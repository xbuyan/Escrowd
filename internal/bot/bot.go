package bot

import (
	"encoding/hex"
	"fmt"
	"github.com/xbuyan/Escrowd/internal/audit"
	"github.com/xbuyan/Escrowd/internal/backup"
	"github.com/xbuyan/Escrowd/internal/bruteforce"
	"github.com/xbuyan/Escrowd/internal/crypto"
	"github.com/xbuyan/Escrowd/internal/escrow"
	"github.com/xbuyan/Escrowd/internal/mpesa"
	"github.com/xbuyan/Escrowd/internal/payment"
	"github.com/xbuyan/Escrowd/internal/ratelimit"
	"github.com/xbuyan/Escrowd/internal/stellar"
	"github.com/xbuyan/Escrowd/internal/store"
	"github.com/xbuyan/Escrowd/internal/validator"
	"github.com/xbuyan/Escrowd/internal/watcher"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

var db *store.Store
var limiter *ratelimit.Limiter
var auditLog *audit.Log
var shield *bruteforce.Shield

// isAdmin reports whether the given Discord user ID is the configured bot
// admin. Extracted as its own function so the check itself — not just the
// handlers built on top of it — has a direct test.
func isAdmin(discordUserID string) bool {
	adminID := os.Getenv("ESCROWD_ADMIN_DISCORD_ID")
	return adminID != "" && discordUserID == adminID
}

func Start() {
	var err error
	db, err = store.New("./data")
	if err != nil {
		fmt.Println("could not open database:", err)
		return
	}
	defer db.Close()

	watcher.Start(db)
	limiter = ratelimit.New(10, time.Hour)
	auditLog = audit.New(db.AuditDB)
	shield = bruteforce.New()
	backup.StartScheduled("./data", "./backups", 24*time.Hour)

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		fmt.Println("DISCORD_TOKEN environment variable not set")
		return
	}

	session, err := discordgo.New("Bot " + token)
	if err != nil {
		fmt.Println("could not create bot session:", err)
		return
	}

	session.AddHandler(messageHandler)
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentDirectMessages

	err = session.Open()
	if err != nil {
		fmt.Println("could not connect to Discord:", err)
		return
	}
	defer session.Close()

	fmt.Println("escrowd bot is running. Press CTRL+C to stop.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	fmt.Println("bot stopped")
}

func messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	if !limiter.Allow(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "you have reached the limit of 10 escrow operations per hour — try again later")
		return
	}

	if !strings.HasPrefix(m.Content, "!escrow") {
		return
	}

	parts := strings.Fields(m.Content)
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow lock/claim/refund/status/dispute/evidence/resolve/history/forget/backup/paid/balance/setaddr")
		return
	}

	command := parts[1]

	switch command {
	case "lock":
		handleLock(s, m, parts)
	case "claim":
		handleClaim(s, m, parts)
	case "refund":
		handleRefund(s, m, parts)
	case "status":
		handleStatus(s, m, parts)
	case "dispute":
		handleDispute(s, m, parts)
	case "evidence":
		handleEvidence(s, m, parts)
	case "resolve":
		handleResolve(s, m, parts)
	case "history":
		handleHistory(s, m, parts)
	case "forget":
		handleForget(s, m, parts)
	case "backup":
		handleBackup(s, m, parts)
	case "paid":
		handlePaid(s, m, parts)
	case "balance":
		handleBalance(s, m, parts)
	case "setaddr":
		handleSetAddr(s, m, parts)
	case "help":
		handleHelp(s, m, parts)
	case "pay":
		handlePay(s, m, parts)
	case "mpesastatus":
		handleMpesaStatus(s, m, parts)
	default:
		s.ChannelMessageSend(m.ChannelID, "unknown command: "+command)
	}
}

func handleLock(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow lock <receiver> <amount>")
		return
	}

	receiver := parts[2]
	amountStr := parts[3]

	if err := validator.ValidateName(receiver); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid receiver: "+err.Error())
		return
	}

	amount, err := strconv.Atoi(amountStr)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "amount must be a number")
		return
	}

	if err := validator.ValidateAmount(amount); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid amount: "+err.Error())
		return
	}

	senderID := m.Author.ID
	senderName := m.Author.Username

	if err := validator.ValidateName(senderName); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid sender name: "+err.Error())
		return
	}

	secret := crypto.GenerateSecret()
	deal := escrow.New(senderID, senderName, receiver, receiver, amount, secret)

	// create a stellar escrow wallet for this deal
	if err := stellar.ValidateNetwork(); err == nil {
		wallet, walletErr := stellar.GenerateEscrowWallet(deal.ID)
		if walletErr == nil {
			deal.StellarWallet = wallet.PublicKey

			// encrypt the wallet secret before storing
			dbKey, keyErr := hex.DecodeString(os.Getenv("ESCROWD_DB_KEY"))
			if keyErr == nil {
				encrypted, encErr := crypto.Encrypt(wallet.SecretKey, dbKey)
				if encErr == nil {
					deal.StellarWalletSecret = encrypted
				}
			}

			// zero the raw wallet secret from memory immediately
			crypto.ZeroString(&wallet.SecretKey)

			auditLog.Record(deal.ID, audit.EventType("STELLAR_WALLET_CREATED"),
				senderID, senderName,
				"wallet: "+wallet.PublicKey)
		}
	}

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save deal")
		return
	}

	auditLog.Record(deal.ID, audit.EventLocked,
		senderID, senderName,
		fmt.Sprintf("locked %d for %s", deal.Amount, deal.ReceiverName))

	stellarMsg := ""
	if deal.StellarWallet != "" {
		stellarMsg = fmt.Sprintf(
			"\n\nStellar escrow wallet: `%s`\nSend %d XLM to this address to activate the deal.\n⚠️ Testnet only — use testnet XLM",
			deal.StellarWallet, deal.Amount)
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Escrow locked!\nID: `%s`\nFrom: %s\nTo: %s\nAmount: %d XLM\nExpires: %s%s\n\nSecret sent to your DMs %s",
		deal.ID, deal.SenderName, deal.ReceiverName, deal.Amount,
		deal.ExpiresAt.Format("2006-01-02 15:04:05"),
		stellarMsg, senderName,
	))

	dm, err := s.UserChannelCreate(m.Author.ID)
	if err != nil {
		fmt.Println("could not create DM:", err)
		return
	}

	s.ChannelMessageSend(dm.ID, fmt.Sprintf(
		"Your secret for escrow `%s`:\n`%s`\n\nKeep this private. Share it with %s only after they deliver.",
		deal.ID, secret, deal.ReceiverName,
	))

	// zero the deal secret from memory after DM is sent
	crypto.ZeroString(&secret)
}

func handleClaim(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow claim <id> <secret>")
		return
	}

	id := parts[2]
	secret := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	if secret == "" {
		s.ChannelMessageSend(m.ChannelID, "secret cannot be empty")
		return
	}

	// check if deal is locked out from too many failed attempts
	locked, remaining := shield.IsLocked(id)
	if locked {
		auditLog.Record(id, audit.EventType("CLAIM_BLOCKED"),
			m.Author.ID, m.Author.Username,
			"claim blocked — deal locked due to too many failed attempts")
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"This escrow is locked due to too many failed claim attempts.\nTry again in %s.",
			formatDuration(remaining),
		))
		return
	}

	// check exponential backoff — must wait between attempts
	mustWait, waitRemaining := shield.MustWait(id)
	if mustWait {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"Too many failed attempts. Please wait %s before trying again.",
			formatDuration(waitRemaining),
		))
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	err = escrow.Claim(&deal, secret)
	if err != nil {
		delay, nowLocked := shield.RecordFailure(id)
		attemptsLeft := shield.AttemptsRemaining(id)

		auditLog.Record(id, audit.EventType("CLAIM_FAILED"),
			m.Author.ID, m.Author.Username,
			fmt.Sprintf("failed claim attempt — %d attempts remaining", attemptsLeft))

		if nowLocked {
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
				"Claim failed: %s\n\nToo many failed attempts. This escrow is now locked for 1 hour.\nIf you are the legitimate receiver, contact the sender.",
				err.Error(),
			))
			return
		}

		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"Claim failed: %s\n\nAttempts remaining: %d\nNext attempt allowed in: %s",
			err.Error(),
			attemptsLeft,
			bruteforce.FormatDelay(delay),
		))
		return
	}

	// successful claim — clear the shield for this deal
	shield.RecordSuccess(id)

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save deal")
		return
	}

	auditLog.Record(deal.ID, audit.EventClaimed,
		m.Author.ID, m.Author.Username,
		"escrow claimed with correct secret")

	// release stellar funds if wallet exists
	stellarMsg := ""
	if deal.StellarWallet != "" && deal.StellarWalletSecret != "" {
		if deal.ReceiverStellarAddr == "" {
			stellarMsg = "\n\n⚠️ No Stellar address set for receiver. Funds remain in escrow wallet.\nReceiver must run: `!escrow setaddr <id> <stellar-address>`"
		} else {
			dbKey, keyErr := hex.DecodeString(os.Getenv("ESCROWD_DB_KEY"))
			if keyErr == nil {
				walletSecret, decErr := crypto.Decrypt(deal.StellarWalletSecret, dbKey)
				if decErr == nil {
					// send the deal amount leaving 1.5 XLM for fees and minimum reserve
					sendAmount := fmt.Sprintf("%d.0000000", deal.Amount)
					txHash, txErr := stellar.SendPayment(
						walletSecret,
						deal.ReceiverStellarAddr,
						sendAmount,
						"escrowd-"+deal.ID[:8],
					)
					crypto.ZeroString(&walletSecret)

					if txErr != nil {
						stellarMsg = "\n\n⚠️ Stellar release failed: " + txErr.Error()
					} else {
						deal.StellarTxHash = txHash
						db.Save(deal)
						auditLog.Record(deal.ID, audit.EventType("STELLAR_RELEASED"),
							m.Author.ID, m.Author.Username,
							"funds released — tx: "+txHash)
						stellarMsg = fmt.Sprintf("\n\n✅ %s XLM released on Stellar\nTransaction: `%s`", sendAmount, txHash)
					}
				}
			}
		}
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Escrow claimed!\nID: `%s`\nStatus: %s%s",
		deal.ID, deal.Status, stellarMsg,
	))
}

func handleRefund(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow refund <id>")
		return
	}

	id := parts[2]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.SenderID != m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "only the sender can refund this escrow")
		return
	}

	err = escrow.Refund(&deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "refund failed: "+err.Error())
		return
	}

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save deal")
		return
	}

	auditLog.Record(deal.ID, audit.EventRefunded,
		m.Author.ID, m.Author.Username,
		"escrow refunded by sender")

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Escrow refunded!\nID: `%s`\nStatus: %s",
		deal.ID, deal.Status,
	))
}

func handleStatus(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow status <id>")
		return
	}

	id := parts[2]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	verified := "✅ verified"
	if !escrow.VerifySignature(deal) {
		verified = "⚠️ TAMPERED — signature mismatch"
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"ID: `%s`\nFrom: %s\nTo: %s\nAmount: %d\nStatus: %s\nExpires: %s\nExpired: %v\nIntegrity: %s",
		deal.ID, deal.SenderName, deal.ReceiverName, deal.Amount,
		deal.Status, deal.ExpiresAt.Format("2006-01-02 15:04:05"),
		escrow.IsExpired(deal), verified,
	))
}

func handleDispute(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow dispute <id> <reason>")
		return
	}

	id := parts[2]
	reason := strings.Join(parts[3:], " ")

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	if err := validator.ValidateReason(reason); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid reason: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.SenderID != m.Author.ID && deal.ReceiverID != m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "only the sender or receiver can dispute this escrow")
		return
	}

	err = escrow.RaiseDispute(&deal, m.Author.ID, m.Author.Username, reason)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "dispute failed: "+err.Error())
		return
	}

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save dispute")
		return
	}

	auditLog.Record(deal.ID, audit.EventDisputed,
		m.Author.ID, m.Author.Username,
		"dispute raised: "+reason)

	// generate payment link for turbo dispute
	reference := fmt.Sprintf("dispute-%s", deal.Dispute.ID)
	payURL, payErr := payment.InitializePayment(
		m.Author.Username+"@escrowd.app",
		6000,
		reference,
		map[string]string{
			"escrow_id":  deal.ID,
			"dispute_id": deal.Dispute.ID,
			"raised_by":  m.Author.Username,
		},
	)

	freeOption := "Free: auto-resolved in 24 hours"
	fastOption := "Fast option unavailable right now"
	if payErr == nil {
		fastOption = fmt.Sprintf("Fast: pay KES 60 for 15-min resolution\n%s", payURL)
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Dispute raised!\nID: `%s`\nDispute ID: `%s`\nRaised by: %s\nReason: %s\n\n"+
			"The escrow is now frozen.\n\n"+
			"Resolution options:\n• %s\n• %s\n\n"+
			"Submit evidence with:\n`!escrow evidence %s <link-to-proof>`",
		deal.ID, deal.Dispute.ID, m.Author.Username, reason,
		freeOption, fastOption, deal.ID,
	))
}

func handleEvidence(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow evidence <id> <link>")
		return
	}

	id := parts[2]
	link := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	if err := validator.ValidateLink(link); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid link: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.SenderID != m.Author.ID && deal.ReceiverID != m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "only the sender or receiver can submit evidence")
		return
	}

	err = escrow.AddEvidence(&deal, m.Author.ID, m.Author.Username, link)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "evidence failed: "+err.Error())
		return
	}

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save evidence")
		return
	}

	auditLog.Record(deal.ID, audit.EventEvidence,
		m.Author.ID, m.Author.Username,
		"evidence submitted: "+link)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Evidence recorded!\nDispute: `%s`\nSubmitted by: %s\nLink: %s\nTotal evidence: %d piece(s)",
		deal.Dispute.ID, m.Author.Username, link, len(deal.Dispute.Evidence),
	))
}

func handleResolve(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow resolve <id> <refund|release>")
		return
	}

	id := parts[2]
	resolution := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	if resolution != "refund" && resolution != "release" {
		s.ChannelMessageSend(m.ChannelID, "resolution must be either 'refund' or 'release'")
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if !isAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "only an escrowd admin can resolve disputes")
		return
	}

	err = escrow.ResolveDispute(&deal, resolution)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "resolve failed: "+err.Error())
		return
	}

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save resolution")
		return
	}

	auditLog.Record(deal.ID, audit.EventResolved,
		m.Author.ID, m.Author.Username,
		"dispute resolved: "+resolution)

	outcome := "funds released to receiver"
	if resolution == "refund" {
		outcome = "funds returned to sender"
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Dispute resolved!\nID: `%s`\nResolution: %s\nOutcome: %s\nFinal status: %s",
		deal.ID, resolution, outcome, deal.Status,
	))
}

func handleHistory(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow history <id>")
		return
	}

	id := parts[2]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	entries, err := auditLog.GetByEscrow(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not retrieve history")
		return
	}

	if len(entries) == 0 {
		s.ChannelMessageSend(m.ChannelID, "no history found for this escrow")
		return
	}

	msg := fmt.Sprintf("Audit trail for `%s`:\n", id)
	for _, e := range entries {
		msg += fmt.Sprintf("• %s — %s by %s at %s\n",
			e.Event, e.Detail, e.ActorName,
			e.Timestamp.Format("2006-01-02 15:04:05"))
	}

	s.ChannelMessageSend(m.ChannelID, msg)
}

func handleForget(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	dm, err := s.UserChannelCreate(m.Author.ID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not open DM")
		return
	}

	count, err := db.DeleteUserData(m.Author.ID)
	if err != nil {
		s.ChannelMessageSend(dm.ID, "could not process data deletion request")
		return
	}

	auditLog.Record(
		"system",
		audit.EventType("USER_DATA_DELETED"),
		m.Author.ID,
		m.Author.Username,
		fmt.Sprintf("user requested data deletion — %d deals anonymized", count),
	)

	s.ChannelMessageSend(dm.ID, fmt.Sprintf(
		"Your data deletion request has been processed.\n\n"+
			"• %d deal(s) have been anonymized\n"+
			"• Your user ID and username have been replaced with 'deleted-user'\n"+
			"• Financial records are retained for legal compliance\n"+
			"• Audit logs retain event timestamps but not your identity\n\n"+
			"This action is irreversible.",
		count,
	))

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"%s your data deletion request has been processed. Check your DMs for details.",
		m.Author.Mention(),
	))
}

func handleBackup(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if !isAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "only an escrowd admin can trigger backups")
		return
	}

	filename, err := backup.Create("./data", "./backups")
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "backup failed: "+err.Error())
		return
	}

	auditLog.Record("system", audit.EventType("BACKUP_CREATED"),
		m.Author.ID, m.Author.Username, "manual backup: "+filename)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Backup created successfully\nFile: `%s`\nBoth databases backed up and compressed.",
		filename,
	))
}

func handlePaid(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow paid <id> <paystack-reference>")
		return
	}

	id := parts[2]
	reference := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.Status != escrow.StatusDisputed {
		s.ChannelMessageSend(m.ChannelID, "no active dispute on this escrow")
		return
	}

	if deal.SenderID != m.Author.ID && deal.ReceiverID != m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "only the sender or receiver can upgrade this dispute")
		return
	}

	paid, err := payment.VerifyPayment(reference)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not verify payment: "+err.Error())
		return
	}

	if !paid {
		s.ChannelMessageSend(m.ChannelID, "payment not confirmed — please complete payment first")
		return
	}

	deal.Dispute.Priority = true
	deal.Dispute.PayReference = reference

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save deal")
		return
	}

	auditLog.Record(deal.ID, audit.EventType("DISPUTE_UPGRADED"),
		m.Author.ID, m.Author.Username,
		"dispute upgraded to priority via payment: "+reference)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Payment confirmed! Dispute `%s` upgraded to priority.\nAn admin will review and resolve within 15 minutes.",
		deal.Dispute.ID,
	))
}

func handleBalance(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow balance <id>")
		return
	}

	id := parts[2]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.StellarWallet == "" {
		s.ChannelMessageSend(m.ChannelID, "no stellar wallet for this deal")
		return
	}

	balance, err := stellar.GetBalance(deal.StellarWallet)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not check balance: "+err.Error())
		return
	}

	funded := "❌ not funded"
	if balance != "0" {
		funded = "✅ funded"
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Stellar wallet for `%s`:\nAddress: `%s`\nBalance: %s XLM\nStatus: %s",
		deal.ID, deal.StellarWallet, balance, funded,
	))
}

func handleSetAddr(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow setaddr <id> <stellar-address>")
		return
	}

	id := parts[2]
	stellarAddr := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	if len(stellarAddr) != 56 || stellarAddr[0] != 'G' {
		s.ChannelMessageSend(m.ChannelID, "invalid Stellar address — must start with G and be 56 characters")
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.ReceiverID != m.Author.ID && deal.SenderID != m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "only the sender or receiver can set the Stellar address")
		return
	}

	deal.ReceiverStellarAddr = stellarAddr

	err = db.Save(deal)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not save address")
		return
	}

	auditLog.Record(deal.ID, audit.EventType("RECEIVER_ADDR_SET"),
		m.Author.ID, m.Author.Username,
		"receiver stellar address set: "+stellarAddr)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Stellar address set for escrow `%s`\nFunds will be released to: `%s`",
		deal.ID, stellarAddr,
	))
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f seconds", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	}
	return fmt.Sprintf("%.1f hours", d.Hours())
}
func handleHelp(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	help := `**escrowd** — Safe peer-to-peer trading on Discord

**How it works:**
1. Alice locks a deal with a secret
2. Bob delivers the goods
3. Alice reveals the secret
4. Bob claims — funds release automatically

**Commands:**
` + "```" + `
!escrow lock <receiver> <amount>
  Create a new escrow deal

!escrow claim <id> <secret>
  Claim a deal with your secret

!escrow refund <id>
  Refund a locked deal (sender only)

!escrow status <id>
  Check deal status and integrity

!escrow balance <id>
  Check Stellar wallet balance

!escrow setaddr <id> <stellar-address>
  Set your Stellar address for fund release

!escrow dispute <id> <reason>
  Raise a dispute — freezes the deal

!escrow evidence <id> <link>
  Submit evidence for a dispute

!escrow paid <id> <reference>
  Confirm Paystack payment for priority dispute

!escrow history <id>
  View full audit trail

!escrow pay <id> <phone>
  Fund deal via M-Pesa STK push

!escrow mpesastatus <id> <checkout-id>
  Check M-Pesa payment status

!escrow forget
  Delete your personal data (GDPR)

!escrow help
  Show this message
` + "```" + `
**Free:** Basic escrow + 24h dispute resolution
**KES 60:** Priority dispute resolved in 15 minutes

Built with Go · github.com/xbuyan/Escrowd`

	s.ChannelMessageSend(m.ChannelID, help)
}
func handlePay(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow pay <id> <phone>\nexample: !escrow pay abc-123 0712345678")
		return
	}

	id := parts[2]
	phone := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	if deal.SenderID != m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "only the sender can initiate payment")
		return
	}

	if deal.Status != escrow.StatusLocked {
		s.ChannelMessageSend(m.ChannelID, "deal is not in locked state")
		return
	}

	formattedPhone := mpesa.FormatPhone(phone)
	if len(formattedPhone) != 12 {
		s.ChannelMessageSend(m.ChannelID, "invalid phone number — use format 0712345678 or 254712345678")
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"Initiating M-Pesa payment...\nPhone: %s\nAmount: KES %d\nCheck your phone for the STK push prompt.",
		formattedPhone, deal.Amount,
	))

	stkResp, err := mpesa.InitiateSTKPush(formattedPhone, deal.Amount, deal.ID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "M-Pesa payment failed: "+err.Error())
		return
	}

	auditLog.Record(deal.ID, audit.EventType("MPESA_STK_INITIATED"),
		m.Author.ID, m.Author.Username,
		"STK push initiated — checkout: "+stkResp.CheckoutRequestID)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
		"M-Pesa prompt sent!\nCheckout ID: `%s`\n\nEnter your M-Pesa PIN on your phone to complete payment.\nOnce paid type:\n`!escrow mpesastatus %s %s`",
		stkResp.CheckoutRequestID, id, stkResp.CheckoutRequestID,
	))
}

func handleMpesaStatus(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "usage: !escrow mpesastatus <id> <checkout-request-id>")
		return
	}

	id := parts[2]
	checkoutID := parts[3]

	if err := validator.ValidateID(id); err != nil {
		s.ChannelMessageSend(m.ChannelID, "invalid ID: "+err.Error())
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "deal not found: "+id)
		return
	}

	queryResp, err := mpesa.QuerySTKStatus(checkoutID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "could not query payment status: "+err.Error())
		return
	}

	if queryResp.ResultCode == "0" {
		deal.StellarFunded = true
		db.Save(deal)

		auditLog.Record(deal.ID, audit.EventType("MPESA_PAYMENT_CONFIRMED"),
			m.Author.ID, m.Author.Username,
			"M-Pesa payment confirmed — checkout: "+checkoutID)

		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"M-Pesa payment confirmed!\nDeal `%s` is now funded.\nBob can now deliver and claim when ready.",
			deal.ID,
		))
	} else {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"Payment status: %s\nResult: %s",
			queryResp.ResponseDescription,
			queryResp.ResultDesc,
		))
	}
}
