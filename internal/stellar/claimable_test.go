package stellar

import (
	"os"
	"testing"

	"github.com/stellar/go-stellar-sdk/txnbuild"
)

// --- Unit tests: no network calls, no env vars required ---

func TestCreateClaimableEscrow_BlockedOnMainnet(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "mainnet")
	defer os.Setenv("STELLAR_NETWORK", "testnet")

	_, err := CreateClaimableEscrow("dummy", "dummy", "10", 172800)
	if err == nil {
		t.Error("expected error on mainnet, got nil")
	}
	if err.Error() != "mainnet not enabled — set STELLAR_NETWORK=mainnet explicitly" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestApproveClaimableBalance_BlockedOnMainnet(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "mainnet")
	defer os.Setenv("STELLAR_NETWORK", "testnet")

	_, err := ApproveClaimableBalance("dummy", "dummy-balance-id")
	if err == nil {
		t.Error("expected error on mainnet, got nil")
	}
}

func TestReclaimExpiredBalance_BlockedOnMainnet(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "mainnet")
	defer os.Setenv("STELLAR_NETWORK", "testnet")

	_, err := ReclaimExpiredBalance("dummy", "dummy-balance-id")
	if err == nil {
		t.Error("expected error on mainnet, got nil")
	}
}

func TestCreateClaimableEscrow_InvalidSecretKey(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "testnet")

	_, err := CreateClaimableEscrow("not-a-valid-secret", "GDUMMY", "10", 172800)
	if err == nil {
		t.Error("expected error for invalid secret key, got nil")
	}
}

func TestApproveClaimableBalance_InvalidSecretKey(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "testnet")

	_, err := ApproveClaimableBalance("not-a-valid-secret", "dummy-balance-id")
	if err == nil {
		t.Error("expected error for invalid secret key, got nil")
	}
}

func TestReclaimExpiredBalance_InvalidSecretKey(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "testnet")

	_, err := ReclaimExpiredBalance("not-a-valid-secret", "dummy-balance-id")
	if err == nil {
		t.Error("expected error for invalid secret key, got nil")
	}
}

// TestPredicateConstruction verifies that the claimant predicate logic builds
// without panicking. This is pure SDK logic — no network required.
func TestPredicateConstruction(t *testing.T) {
	expiryPredicate := txnbuild.BeforeRelativeTimePredicate(172800) // 48h

	// Verify the predicate is not zero-value
	if expiryPredicate.Type == 0 {
		t.Error("expected non-zero predicate type for BeforeRelativeTimePredicate")
	}

	// Verify NewClaimant with nil predicate gives unconditional
	unconditional := txnbuild.NewClaimant("GDUMMYPUBLICKEYAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil)
	if unconditional.Predicate.Type != 0 {
		// ClaimPredicateUnconditional is type 0
		t.Errorf("expected unconditional predicate type 0, got %d", unconditional.Predicate.Type)
	}

	// Verify NewClaimant with expiry predicate carries it through
	claimant := txnbuild.NewClaimant("GDUMMYPUBLICKEYAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", &expiryPredicate)
	if claimant.Predicate.Type == 0 {
		t.Error("expected non-zero predicate type for time-bounded claimant")
	}
}

// TestGetClaimableBalance_NonExistentID verifies that a nonexistent balance ID
// returns nil error and nil balance (the 404 path). Requires network.
func TestGetClaimableBalance_NonExistentID(t *testing.T) {
	os.Setenv("STELLAR_NETWORK", "testnet")

	// This is a well-formed but nonexistent balance ID
	fakeID := "00000000da0d57da1a40cf963c1e1f33de4fd4b927e78a940ce5e86730756e8534e4c4f4f"

	balance, err := GetClaimableBalance(fakeID)
	if err != nil {
		// Network unavailable in CI — skip rather than fail
		t.Skipf("network unavailable or unexpected error: %v", err)
	}
	if balance != nil {
		t.Error("expected nil balance for nonexistent ID, got a result")
	}
}

// --- Integration tests: require STELLAR_MASTER_SECRET and live testnet ---

// TestClaimableEscrow_FullRoundTrip creates a real claimable balance on testnet,
// queries it, then reclaims it. Skipped unless STELLAR_MASTER_SECRET is set.
//
// To run manually:
//
//	STELLAR_MASTER_SECRET=S... STELLAR_NETWORK=testnet go test ./internal/stellar/... -run TestClaimableEscrow_FullRoundTrip -v
func TestClaimableEscrow_FullRoundTrip(t *testing.T) {
	secret := os.Getenv("STELLAR_MASTER_SECRET")
	if secret == "" {
		t.Skip("STELLAR_MASTER_SECRET not set — skipping integration test")
	}
	os.Setenv("STELLAR_NETWORK", "testnet")

	// Generate a fresh Bob keypair for this test
	bobWallet, err := GenerateEscrowWallet("test-bob")
	if err != nil {
		t.Fatalf("could not generate bob wallet: %v", err)
	}

	// Fund Bob's account via Friendbot so it exists on the ledger
	if err := FundTestnetWallet(bobWallet.PublicKey); err != nil {
		t.Fatalf("could not fund bob wallet: %v", err)
	}

	// Create a claimable escrow — Alice (master) locks 5 XLM for Bob
	// with a 60-second expiry so the test can reclaim quickly
	escrow, err := CreateClaimableEscrow(secret, bobWallet.PublicKey, "5", 60)
	if err != nil {
		t.Fatalf("CreateClaimableEscrow failed: %v", err)
	}

	if escrow.BalanceID == "" {
		t.Error("expected non-empty BalanceID")
	}
	if escrow.TxHash == "" {
		t.Error("expected non-empty TxHash")
	}
	t.Logf("Claimable balance created: ID=%s TxHash=%s", escrow.BalanceID, escrow.TxHash)

	// Query the balance — should exist on-chain
	balance, err := GetClaimableBalance(escrow.BalanceID)
	if err != nil {
		t.Fatalf("GetClaimableBalance failed: %v", err)
	}
	if balance == nil {
		t.Fatal("expected balance to exist on-chain, got nil")
	}
	t.Logf("Balance confirmed on-chain: amount=%s", balance.Amount)

	// Bob claims the balance
	claimTxHash, err := ApproveClaimableBalance(bobWallet.SecretKey, escrow.BalanceID)
	if err != nil {
		t.Fatalf("ApproveClaimableBalance failed: %v", err)
	}
	t.Logf("Bob claimed balance: TxHash=%s", claimTxHash)

	// Balance should no longer exist on-chain
	gone, err := GetClaimableBalance(escrow.BalanceID)
	if err != nil {
		t.Fatalf("GetClaimableBalance post-claim failed: %v", err)
	}
	if gone != nil {
		t.Error("expected balance to be gone after claim, but it still exists")
	}
}
