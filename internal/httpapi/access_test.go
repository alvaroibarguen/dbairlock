package httpapi

import (
	"slices"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/identity"
)

// goName is the name oapi-codegen gives an operationId.
func goName(operationID string) string {
	return strings.ToUpper(operationID[:1]) + operationID[1:]
}

// Every operation in the contract declares an access level, and every
// declaration names a real operation.
func TestEveryOperationDeclaresAccess(t *testing.T) {
	doc := loadContract(t)
	seen := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			name := goName(op.OperationID)
			seen[name] = true
			a, ok := OperationAccess(name)
			if !ok {
				t.Errorf("%s %s (%s) has no access level in operationAccess", method, path, op.OperationID)
				continue
			}
			switch a.Level {
			case LevelPublic, LevelSetup, LevelDev:
				if a.Action != "" {
					t.Errorf("%s: level %s cannot require an action", name, a.Level)
				}
			case LevelUser:
				if a.Action != "" && !slices.Contains(identity.Actions(), a.Action) {
					t.Errorf("%s: unknown action %q", name, a.Action)
				}
			default:
				t.Errorf("%s: level %q is not valid for a REST operation", name, a.Level)
			}
			// Only reads, sign-in and setup may be reachable without a session.
			if a.Level == LevelPublic && method != "GET" && name != "SignOut" {
				t.Errorf("%s %s is public but mutates", method, path)
			}
		}
	}
	for name := range operationAccess {
		if !seen[name] {
			t.Errorf("operationAccess declares %s, which is not in api/openapi.yaml", name)
		}
	}
}
