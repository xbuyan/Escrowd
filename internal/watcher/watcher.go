package watcher

import (
	"fmt"
	"github.com/xbuyan/Escrowd/internal/escrow"
	"github.com/xbuyan/Escrowd/internal/stellar"
	"github.com/xbuyan/Escrowd/internal/store"
	"time"
)

func Start(db *store.Store) {
	go func() {
		fmt.Println("expiry watcher started — checking every hour")
		for {
			runCheck(db)
			time.Sleep(1 * time.Hour)
		}
	}()

	go func() {
		fmt.Println("stellar payment watcher started — checking every 30 seconds")
		for {
			runStellarCheck(db)
			time.Sleep(30 * time.Second)
		}
	}()
}

func runCheck(db *store.Store) {
	ids, err := db.ListIDs()
	if err != nil {
		fmt.Println("watcher error listing deals:", err)
		return
	}

	expired := 0
	for _, id := range ids {
		deal, err := db.Get(id)
		if err != nil {
			continue
		}

		if deal.Status == escrow.StatusLocked && escrow.IsExpired(deal) {
			err = escrow.Refund(&deal)
			if err != nil {
				continue
			}
			err = db.Save(deal)
			if err != nil {
				continue
			}
			fmt.Printf("auto-refunded expired deal: %s (sender: %s)\n", deal.ID, deal.SenderName)
			expired++
		}

		if deal.Status == escrow.StatusDisputed && escrow.IsExpired(deal) {
			err = escrow.ResolveDispute(&deal, "refund")
			if err != nil {
				continue
			}
			err = db.Save(deal)
			if err != nil {
				continue
			}
			fmt.Printf("auto-resolved expired dispute: %s (sender: %s)\n", deal.ID, deal.SenderName)
			expired++
		}
	}

	if expired > 0 {
		fmt.Printf("watcher: auto-refunded %d expired deal(s)\n", expired)
	}
}

func runStellarCheck(db *store.Store) {
	ids, err := db.ListIDs()
	if err != nil {
		return
	}

	for _, id := range ids {
		deal, err := db.Get(id)
		if err != nil {
			continue
		}

		// only check deals with a stellar wallet that are not yet funded
		if deal.StellarWallet == "" || deal.StellarFunded {
			continue
		}

		if deal.Status != escrow.StatusLocked {
			continue
		}

		// check if the wallet has received funds
		balance, err := stellar.GetBalance(deal.StellarWallet)
		if err != nil {
			continue
		}

		if balance == "0" {
			continue
		}

		// funds received — mark as funded
		deal.StellarFunded = true
		err = db.Save(deal)
		if err != nil {
			continue
		}

		fmt.Printf("stellar payment detected for deal %s — balance: %s XLM\n", deal.ID, balance)
	}
}
