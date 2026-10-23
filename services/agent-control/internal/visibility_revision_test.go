package control

import (
	"context"
	"testing"
)

func TestEquivalentVisibilityPatchCannotRestoreModeratorRemoval(t *testing.T) {
	for _, ids := range [][]string{{"ws1", "ws2"}, {"ws2", "ws1", "ws1"}} {
		t.Run(ids[0], func(t *testing.T) {
			ctx := context.Background()
			s, f, _ := setupControl(t)
			a := createAgent(t, s, []string{"ws1", "ws2"})
			f.members[a.ID] = []string{"ws2"}
			got, err := s.Update(ctx, "owner", a.ID, Changes{Name: ptr("Renamed"), WorkspaceIDs: &ids})
			if err != nil {
				t.Fatal(err)
			}
			if contains(f.members[a.ID], "ws1") || contains(got.WorkspaceIDs, "ws1") {
				t.Fatalf("equivalent visibility restored removed membership: %#v actual=%v", got, f.members[a.ID])
			}
			// Reconciliation has now removed ws1 from selected visibility. A later
			// genuine owner change remains able to select it explicitly.
			revision := got.MembershipRevision
			got, err = s.Update(ctx, "owner", a.ID, Changes{WorkspaceIDs: ptr([]string{"ws1", "ws2"})})
			if err != nil || !contains(f.members[a.ID], "ws1") || got.MembershipRevision <= revision {
				t.Fatal("genuine visibility change did not join", got, err)
			}
		})
	}
}
