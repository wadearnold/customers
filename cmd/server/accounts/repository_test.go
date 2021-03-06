// Copyright 2020 The Moov Authors
// Use of this source code is governed by an Apache License
// license that can be found in the LICENSE file.

package accounts

import (
	"testing"

	"github.com/moov-io/base"
	"github.com/moov-io/customers/admin"
	"github.com/moov-io/customers/client"
	"github.com/moov-io/customers/internal/database"
	"github.com/moov-io/customers/pkg/secrets"

	"github.com/go-kit/kit/log"
)

type testAccountRepository struct {
	Repository

	db *database.TestSQLiteDB
}

func setupTestAccountRepository(t *testing.T) *testAccountRepository {
	db := database.CreateTestSqliteDB(t)
	repo := NewRepo(log.NewNopLogger(), db.DB)

	t.Cleanup(func() {
		db.Close()
		repo.Close()
	})

	return &testAccountRepository{Repository: repo, db: db}
}

func TestRepository(t *testing.T) {
	customerID, userID := base.ID(), base.ID()
	repo := setupTestAccountRepository(t)

	// initial read, find no accounts
	accounts, err := repo.getCustomerAccounts(customerID)
	if len(accounts) != 0 || err != nil {
		t.Fatalf("got accounts=%#v error=%v", accounts, err)
	}

	// create account
	acct, err := repo.createCustomerAccount(customerID, userID, &createAccountRequest{
		AccountNumber: "123",
		RoutingNumber: "987654320",
		Type:          client.CHECKING,
	})
	if err != nil {
		t.Fatal(err)
	}

	// read after creating
	accounts, err = repo.getCustomerAccounts(customerID)
	if len(accounts) != 1 || err != nil {
		t.Fatalf("got accounts=%#v error=%v", accounts, err)
	}
	if accounts[0].AccountID != acct.AccountID {
		t.Errorf("accounts[0].AccountID=%s acct.AccountID=%s", accounts[0].AccountID, acct.AccountID)
	}

	// delete, expect no accounts
	if err := repo.deactivateCustomerAccount(acct.AccountID); err != nil {
		t.Fatal(err)
	}
	accounts, err = repo.getCustomerAccounts(customerID)
	if len(accounts) != 0 || err != nil {
		t.Fatalf("got accounts=%#v error=%v", accounts, err)
	}
}

func TestRepository__getEncryptedAccountNumber(t *testing.T) {
	customerID, userID := base.ID(), base.ID()
	repo := setupTestAccountRepository(t)

	keeper := secrets.TestStringKeeper(t)

	// create account
	req := &createAccountRequest{
		AccountNumber: "123",
		RoutingNumber: "987654320",
		Type:          client.CHECKING,
	}
	if err := req.disfigure(keeper); err != nil {
		t.Fatal(err)
	}
	acct, err := repo.createCustomerAccount(customerID, userID, req)
	if err != nil {
		t.Fatal(err)
	}

	// read encrypted account number
	encrypted, err := repo.getEncryptedAccountNumber(customerID, acct.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "" {
		t.Error("missing encrypted account number")
	}
}

func TestRepository__updateAccountStatus(t *testing.T) {
	customerID, userID := base.ID(), base.ID()
	repo := setupTestAccountRepository(t)

	keeper := secrets.TestStringKeeper(t)

	// create account
	req := &createAccountRequest{
		AccountNumber: "123",
		RoutingNumber: "987654320",
		Type:          client.CHECKING,
	}
	if err := req.disfigure(keeper); err != nil {
		t.Fatal(err)
	}
	acct, err := repo.createCustomerAccount(customerID, userID, req)
	if err != nil {
		t.Fatal(err)
	}

	// update status
	if err := repo.updateAccountStatus(acct.AccountID, admin.VALIDATED); err != nil {
		t.Fatal(err)
	}

	// check status after update
	acct, err = repo.getCustomerAccount(customerID, acct.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Status != client.VALIDATED {
		t.Errorf("unexpected status: %s", acct.Status)
	}
}
