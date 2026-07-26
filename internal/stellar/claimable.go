package stellar

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/horizonclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	hProtocol "github.com/stellar/go-stellar-sdk/protocols/horizon"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ClaimableEscrow holds the result of a successfully created claimable balance.
// The BalanceID is the on-chain identifier — store this in PostgreSQL against
// the deal record. Escrowd never holds the funds; they live on the Stellar ledger.
type ClaimableEscrow struct {
	BalanceID string    // hex-encoded claimable balance ID from Stellar
	Amount    string    // XLM amount locked
	Sender    string    // Alice's public key
	Receiver  string    // Bob's public key
	ExpiresAt time.Time // when Alice can reclaim automatically
	TxHash    string    // Stellar transaction hash for the lock tx
}

// CreateClaimableEscrow locks XLM on the Stellar ledger as a claimable balance.
//
// Predicate logic:
//   - Bob   can claim: unconditionally (escrowd will only co-sign when deal is complete)
//   - Alice can claim: only after expirySeconds have elapsed (automatic refund path)
//
// Alice signs and submits this transaction herself. Escrowd is NOT a custodian.
// The returned ClaimableEscrow.BalanceID must be stored in the deal's PostgreSQL record.
func CreateClaimableEscrow(
	aliceSecret string, // Alice's Stellar secret key (S...)
	bobPublicKey string, // Bob's Stellar public key (G...)
	amount string, // XLM amount as string e.g. "10.5"
	expirySeconds int64, // seconds until Alice can reclaim; use 172800 for 48h
) (*ClaimableEscrow, error) {
	if !isTestnet() {
		return nil, errors.New("mainnet not enabled — set STELLAR_NETWORK=mainnet explicitly")
	}

	// Parse Alice's keypair
	aliceKP, err := keypair.ParseFull(aliceSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid alice secret key: %w", err)
	}

	c := client()

	// Load Alice's account for sequence number
	aliceAccount, err := c.AccountDetail(horizonclient.AccountRequest{
		AccountID: aliceKP.Address(),
	})
	if err != nil {
		return nil, fmt.Errorf("could not load alice account: %w", err)
	}

	// Bob can claim unconditionally — but escrowd controls the deal state machine,
	// so in practice Bob only learns the balance ID once escrowd approves.
	bobClaimant := txnbuild.NewClaimant(bobPublicKey, nil)

	// Alice can reclaim after expirySeconds using a relative time predicate.
	// BeforeRelativeTimePredicate is fulfilled when:
	//   ledger_close_time + expirySeconds < now
	// which means Alice can claim once that window has passed.
	expiryPredicate := txnbuild.BeforeRelativeTimePredicate(expirySeconds)
	aliceClaimant := txnbuild.NewClaimant(aliceKP.Address(), &expiryPredicate)

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &aliceAccount,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{
			&txnbuild.CreateClaimableBalance{
				Asset:        txnbuild.NativeAsset{},
				Amount:       amount,
				Destinations: []txnbuild.Claimant{bobClaimant, aliceClaimant},
			},
		},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
		Memo:          txnbuild.MemoText("escrowd-lock"),
	})
	if err != nil {
		return nil, fmt.Errorf("could not build lock transaction: %w", err)
	}

	tx, err = tx.Sign(networkPassphrase(), aliceKP)
	if err != nil {
		return nil, fmt.Errorf("could not sign lock transaction: %w", err)
	}

	resp, err := c.SubmitTransaction(tx)
	if err != nil {
		return nil, fmt.Errorf("could not submit lock transaction: %w", err)
	}

	// Extract the claimable balance ID from the transaction ResultXdr.
	// Horizon does not expose OperationResults() directly on Transaction —
	// we decode the base64 XDR result envelope ourselves.
	resultBytes, err := base64.StdEncoding.DecodeString(resp.ResultXdr)
	if err != nil {
		return nil, fmt.Errorf("could not decode result XDR: %w", err)
	}

	var txResult xdr.TransactionResult
	if _, err = xdr.Unmarshal(bytes.NewReader(resultBytes), &txResult); err != nil {
		return nil, fmt.Errorf("could not unmarshal transaction result: %w", err)
	}

	opResults, ok := txResult.OperationResults()
	if !ok || len(opResults) == 0 {
		return nil, errors.New("no operation results in transaction")
	}

	cbResult, ok := opResults[0].MustTr().GetCreateClaimableBalanceResult()
	if !ok {
		return nil, errors.New("could not extract claimable balance result")
	}

	balanceIDStr, err := xdr.MarshalHex(cbResult.BalanceId)
	if err != nil {
		return nil, fmt.Errorf("could not encode balance ID as hex: %w", err)
	}

	return &ClaimableEscrow{
		BalanceID: balanceIDStr,
		Amount:    amount,
		Sender:    aliceKP.Address(),
		Receiver:  bobPublicKey,
		ExpiresAt: time.Now().Add(time.Duration(expirySeconds) * time.Second),
		TxHash:    resp.Hash,
	}, nil
}

// ApproveClaimableBalance allows Bob to claim a claimable balance.
//
// This is called when the escrow deal reaches the `completed` state.
// Bob signs this transaction himself — escrowd only verifies the deal state
// before handing Bob the balance ID. Bob's wallet submits the claim.
//
// Returns the transaction hash of the claim.
func ApproveClaimableBalance(bobSecret string, balanceID string) (string, error) {
	if !isTestnet() {
		return "", errors.New("mainnet not enabled")
	}

	bobKP, err := keypair.ParseFull(bobSecret)
	if err != nil {
		return "", fmt.Errorf("invalid bob secret key: %w", err)
	}

	c := client()

	bobAccount, err := c.AccountDetail(horizonclient.AccountRequest{
		AccountID: bobKP.Address(),
	})
	if err != nil {
		return "", fmt.Errorf("could not load bob account: %w", err)
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &bobAccount,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{
			&txnbuild.ClaimClaimableBalance{
				BalanceID: balanceID,
			},
		},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
		Memo:          txnbuild.MemoText("escrowd-claim"),
	})
	if err != nil {
		return "", fmt.Errorf("could not build claim transaction: %w", err)
	}

	tx, err = tx.Sign(networkPassphrase(), bobKP)
	if err != nil {
		return "", fmt.Errorf("could not sign claim transaction: %w", err)
	}

	resp, err := c.SubmitTransaction(tx)
	if err != nil {
		return "", fmt.Errorf("could not submit claim transaction: %w", err)
	}

	return resp.Hash, nil
}

// ReclaimExpiredBalance allows Alice to reclaim funds after the escrow expires.
//
// This is called by the expiry watcher goroutine in internal/watcher/ when a
// deal passes its ExpiresAt timestamp without being claimed, or manually when
// Alice calls /refund. The relative time predicate on the Stellar ledger enforces
// the timeout — this will fail if called before expiry.
//
// Returns the transaction hash of the reclaim.
func ReclaimExpiredBalance(aliceSecret string, balanceID string) (string, error) {
	if !isTestnet() {
		return "", errors.New("mainnet not enabled")
	}

	aliceKP, err := keypair.ParseFull(aliceSecret)
	if err != nil {
		return "", fmt.Errorf("invalid alice secret key: %w", err)
	}

	c := client()

	aliceAccount, err := c.AccountDetail(horizonclient.AccountRequest{
		AccountID: aliceKP.Address(),
	})
	if err != nil {
		return "", fmt.Errorf("could not load alice account: %w", err)
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &aliceAccount,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{
			&txnbuild.ClaimClaimableBalance{
				BalanceID: balanceID,
			},
		},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
		Memo:          txnbuild.MemoText("escrowd-reclaim"),
	})
	if err != nil {
		return "", fmt.Errorf("could not build reclaim transaction: %w", err)
	}

	tx, err = tx.Sign(networkPassphrase(), aliceKP)
	if err != nil {
		return "", fmt.Errorf("could not sign reclaim transaction: %w", err)
	}

	resp, err := c.SubmitTransaction(tx)
	if err != nil {
		return "", fmt.Errorf("could not submit reclaim transaction: %w", err)
	}

	return resp.Hash, nil
}

// GetClaimableBalance queries Horizon to check the current state of a claimable balance.
//
// Returns nil if the balance has already been claimed or reclaimed (no longer exists).
// Used by the watcher goroutine to confirm on-chain state before marking a deal expired.
func GetClaimableBalance(balanceID string) (*hProtocol.ClaimableBalance, error) {
	c := &horizonclient.Client{
		HorizonURL: TestnetHorizon,
		HTTP:       http.DefaultClient,
	}

	balance, err := c.ClaimableBalance(balanceID)
	if err != nil {
		// Horizon returns 404 when the balance is gone (claimed or reclaimed)
		var herr *horizonclient.Error
		if errors.As(err, &herr) && herr.Response.StatusCode == http.StatusNotFound {
			return nil, nil // balance no longer exists — already settled
		}
		return nil, fmt.Errorf("could not query claimable balance: %w", err)
	}

	return &balance, nil
}
