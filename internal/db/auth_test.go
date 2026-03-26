package db

import (
	"database/sql"
	"testing"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestAdminExists_Empty(t *testing.T) {
	db := newTestDB(t)
	exists, err := AdminExists(db)
	if err != nil {
		t.Fatalf("AdminExists error: %v", err)
	}
	if exists {
		t.Error("expected no admin to exist on fresh DB")
	}
}

func TestCreateAdmin_Success(t *testing.T) {
	db := newTestDB(t)

	if err := CreateAdmin(db, "admin", "strongpassword123"); err != nil {
		t.Fatalf("CreateAdmin error: %v", err)
	}

	exists, err := AdminExists(db)
	if err != nil {
		t.Fatalf("AdminExists error: %v", err)
	}
	if !exists {
		t.Error("expected admin to exist after creation")
	}
}

func TestCreateAdmin_DuplicateRejected(t *testing.T) {
	db := newTestDB(t)

	if err := CreateAdmin(db, "admin", "password123"); err != nil {
		t.Fatalf("first CreateAdmin error: %v", err)
	}

	err := CreateAdmin(db, "admin2", "password456")
	if err == nil {
		t.Error("expected error when creating second admin")
	}
}

func TestValidateAdmin_CorrectPassword(t *testing.T) {
	db := newTestDB(t)
	CreateAdmin(db, "admin", "correcthorse")

	user, err := ValidateAdmin(db, "admin", "correcthorse")
	if err != nil {
		t.Fatalf("ValidateAdmin error: %v", err)
	}
	if user == nil {
		t.Fatal("expected valid user, got nil")
	}
	if user.Username != "admin" {
		t.Errorf("username = %q, want admin", user.Username)
	}
}

func TestValidateAdmin_WrongPassword(t *testing.T) {
	db := newTestDB(t)
	CreateAdmin(db, "admin", "correcthorse")

	user, err := ValidateAdmin(db, "admin", "wrongpassword")
	if err != nil {
		t.Fatalf("ValidateAdmin error: %v", err)
	}
	if user != nil {
		t.Error("expected nil user for wrong password")
	}
}

func TestValidateAdmin_UnknownUser(t *testing.T) {
	db := newTestDB(t)
	CreateAdmin(db, "admin", "password")

	user, err := ValidateAdmin(db, "nobody", "password")
	if err != nil {
		t.Fatalf("ValidateAdmin error: %v", err)
	}
	if user != nil {
		t.Error("expected nil user for unknown username")
	}
}

func TestResetAdminPassword(t *testing.T) {
	db := newTestDB(t)
	CreateAdmin(db, "admin", "oldpassword")

	if err := ResetAdminPassword(db, "admin", "newpassword"); err != nil {
		t.Fatalf("ResetAdminPassword error: %v", err)
	}

	user, _ := ValidateAdmin(db, "admin", "oldpassword")
	if user != nil {
		t.Error("old password should no longer work")
	}

	user, err := ValidateAdmin(db, "admin", "newpassword")
	if err != nil {
		t.Fatalf("ValidateAdmin error: %v", err)
	}
	if user == nil {
		t.Error("new password should work")
	}
}

func TestGetJWTSecret_GeneratesOnce(t *testing.T) {
	db := newTestDB(t)

	secret1, err := GetJWTSecret(db)
	if err != nil {
		t.Fatalf("GetJWTSecret error: %v", err)
	}
	if len(secret1) != 64 {
		t.Errorf("secret length = %d, want 64", len(secret1))
	}

	secret2, err := GetJWTSecret(db)
	if err != nil {
		t.Fatalf("GetJWTSecret second call error: %v", err)
	}
	if secret1 != secret2 {
		t.Error("expected same secret on second call")
	}
}
