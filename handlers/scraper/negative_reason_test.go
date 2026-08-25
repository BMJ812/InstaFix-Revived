package handlers

import (
	"errors"
	"strings"
	"testing"
)

func TestNegativeReasonPreservesRestrictionDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
		wantText   string
	}{
		{
			name:       "age restriction",
			err:        errors.Join(ErrRestricted, errors.New("People under 21 can't see this content (MIN_AGE_ACCOUNT)")),
			wantReason: "age_restricted_21",
			wantText:   "under 21",
		},
		{
			name:       "geoblock",
			err:        errors.Join(ErrRestricted, errors.New("geoblock_required")),
			wantReason: "geoblock",
			wantText:   "geoblock",
		},
		{
			name:       "private",
			err:        errors.Join(ErrNotFound, errors.New("private media")),
			wantReason: "private",
			wantText:   "private",
		},
		{
			name:       "deleted",
			err:        errors.Join(ErrNotFound, errors.New("deleted media")),
			wantReason: "deleted",
			wantText:   "deleted",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := negativeReason(tc.err)
			if !ok || reason != tc.wantReason {
				t.Fatalf("negativeReason() = %q, %v; want %q, true", reason, ok, tc.wantReason)
			}
			restored := errorForNegativeReason(reason)
			if !strings.Contains(strings.ToLower(restored.Error()), tc.wantText) {
				t.Fatalf("restored error = %q, want substring %q", restored, tc.wantText)
			}
		})
	}
}
