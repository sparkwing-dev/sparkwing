package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestStoreDataCreditSettingsUseFinalSchemaAndSurviveRestart(t *testing.T) {
	s := authorityStore(t)
	s.clock = func() time.Time { return time.Unix(1000, 0).UTC() }
	path := blobAccountingDiskStore(t, s)
	initial, err := s.CreditSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if initial.RateMicroPerSecond != DefaultCreditRateMicro || initial.RateTableSet || initial.GraceSeconds != DefaultCreditGraceSeconds || initial.MaxChargeSeconds != DefaultCreditMaxChargeSeconds || initial.WarmCPUClassCores != DefaultWarmCPUClassCores || initial.StorageRateMicroPerGBDay != 0 || initial.StorageFreeAllowanceBytes != 0 {
		t.Fatalf("defaults=%+v", initial)
	}
	table := CreditRateTable{{Cores: 8, MicroPerSecond: 40000}, {Cores: 4, MicroPerSecond: 20000}, {Cores: 2, MicroPerSecond: 10000}}
	warm, grace, capSeconds, rent, free := int64(4), int64(0), int64(45), int64(123), int64(1<<30)
	want, err := s.SetOperatorCreditSettings(t.Context(), CreditSettingsUpdate{
		RateTable: &table, WarmCPUClassCores: &warm, GraceSeconds: &grace,
		MaxChargeSeconds: &capSeconds, StorageRateMicroPerGBDay: &rent, StorageFreeAllowanceBytes: &free,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !want.RateTableSet || want.RateMicroPerSecond != 20000 || !reflect.DeepEqual(want.RateTable, table.Sorted()) || want.WarmCPUClassCores != warm || want.GraceSeconds != grace || want.MaxChargeSeconds != capSeconds || want.StorageRateMicroPerGBDay != rent || want.StorageFreeAllowanceBytes != free {
		t.Fatalf("stored settings=%+v", want)
	}
	rows, err := s.db.Query("SELECT key,value FROM settings")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(raw) || bytes.HasPrefix(raw, []byte(`"`)) {
			t.Errorf("%s is not typed JSON: %s", key, raw)
		}
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("persisted settings=%d, want 7", count)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	got, err := s.CreditSettings(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened settings=%+v, want %+v: %v", got, want, err)
	}
	if got, err := s.WarmCPUClassCores(t.Context()); err != nil || got != warm {
		t.Fatalf("warm CPU class=%d, want %d: %v", got, warm, err)
	}
}

func TestStoreDataCreditSettingsCorruptionNeverChoosesAnotherPrice(t *testing.T) {
	for _, tc := range []struct{ key, raw string }{
		{metaKeyCreditRateMicro, `"20000"`},
		{metaKeyCreditRateMicro, `null`},
		{metaKeyCreditRateMicro, `1.5`},
		{metaKeyCreditRateMicro, `+20000`},
		{metaKeyCreditRateMicro, ``},
		{metaKeyCreditRateMicro, `0`},
		{metaKeyCreditRateMicro, `1000000000001`},
		{metaKeyCreditRateMicro, `9223372036854775808`},
		{metaKeyCreditGraceSecs, `-1`},
		{metaKeyCreditMaxCharge, `0`},
		{metaKeyWarmCPUClass, `-1`},
		{metaKeyStorageRateMicroPerGBDay, `-1`},
		{metaKeyStorageFreeAllowanceBytes, `-1`},
		{metaKeyCreditRateTable, ``},
		{metaKeyCreditRateTable, `null`},
		{metaKeyCreditRateTable, `[]`},
	} {
		t.Run(tc.key+"/"+tc.raw, func(t *testing.T) {
			s := authorityStore(t)
			rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES (?,?)", tc.key, []byte(tc.raw))
			if _, err := s.CreditSettings(t.Context()); !errors.Is(err, ErrInvalidCreditSetting) {
				t.Fatalf("corrupt setting read as defaults: %v", err)
			}
		})
	}
}

func TestStoreDataCreditSettingsFailureRollsBackBatch(t *testing.T) {
	s := authorityStore(t)
	before, err := s.CreditSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_rate_table BEFORE INSERT ON settings WHEN NEW.key='credit_rate_table' BEGIN SELECT RAISE(ABORT,'rate table refused'); END`)
	grace := int64(0)
	table := CreditRateTable{{Cores: 4, MicroPerSecond: 20000}}
	if _, err := s.SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table, GraceSeconds: &grace}); err == nil {
		t.Fatal("rejected rate table committed")
	}
	got, err := s.CreditSettings(t.Context())
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("failed batch changed settings=%+v: %v", got, err)
	}
	rewriteSchemaCount(t, s.db, "settings", 0)
}

func TestStoreDataCreditSettingsCannotReplaceSealedValues(t *testing.T) {
	s := authorityStore(t)
	original := []byte(`20000`)
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value,sealed) VALUES (?,?,1)", metaKeyCreditRateMicro, original)
	if _, err := s.CreditSettings(t.Context()); !errors.Is(err, ErrInvalidCreditSetting) {
		t.Fatalf("sealed price read=%v", err)
	}
	rate, grace := int64(30000), int64(0)
	if _, err := s.SetCreditSettings(t.Context(), CreditSettingsUpdate{RateMicroPerSecond: &rate, GraceSeconds: &grace}); err == nil {
		t.Fatal("sealed setting was overwritten")
	}
	var raw []byte
	var sealed int
	if err := s.db.QueryRow("SELECT value,sealed FROM settings WHERE key=?", metaKeyCreditRateMicro).Scan(&raw, &sealed); err != nil || !bytes.Equal(raw, original) || sealed != 1 {
		t.Fatalf("sealed value changed: %s,%d: %v", raw, sealed, err)
	}
	rewriteSchemaCount(t, s.db, "settings", 1)
}

func TestStoreDataCreditSettingsIndependentWritersPreserveRateAuthority(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	table := CreditRateTable{{Cores: 4, MicroPerSecond: 20000}}
	scalar := int64(99000)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- s.SetCreditRateTable(t.Context(), table)
	}()
	go func() {
		<-start
		_, err := other.SetOperatorCreditSettings(t.Context(), CreditSettingsUpdate{RateMicroPerSecond: &scalar})
		results <- err
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil && !errors.Is(err, ErrInvalidCreditSetting) {
			t.Fatal(err)
		}
	}
	got, err := s.CreditSettings(t.Context())
	if err != nil || !got.RateTableSet || got.RateMicroPerSecond != 20000 {
		t.Fatalf("concurrent settings=%+v: %v", got, err)
	}
}
