package scaling

import (
	"encoding/json"
	"os"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestSharedHibernateRuleCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/hibernate.json")
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		Cases []struct {
			Name  string                 `json:"name"`
			Rule  finopsv1.HibernateRule `json:"rule"`
			Valid bool                   `json:"valid"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &shared); err != nil {
		t.Fatal(err)
	}
	for _, c := range shared.Cases {
		err := ValidateHibernate([]finopsv1.HibernateRule{c.Rule})
		if (err == nil) != c.Valid {
			t.Errorf("%s: ValidateHibernate = %v, want valid %v", c.Name, err, c.Valid)
		}
	}
}
