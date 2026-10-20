package store

import "testing"

func TestCreditReferenceCannotUseAnEmptyTenantAsWildcard(t *testing.T) {
	s, _ := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_at) VALUES ('a','grant','free',1,'reference',1)`)
	for _, team := range []Team{"", "other", "a"} {
		err := s.inTx(t.Context(), func(tx *storeTx) error {
			grant, found, err := creditGrantByReferenceTx(t.Context(), tx, team, CreditGrantFree, "reference")
			if err == nil && (found != (team == "a") || found && grant.Team != team) {
				t.Errorf("team %q read grant %+v, found=%v", team, grant, found)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
