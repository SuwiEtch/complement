// +build msc2946

package tests

import (
	"fmt"
	"testing"

	"github.com/matrix-org/complement/internal/b"
	"github.com/matrix-org/complement/internal/match"
	"github.com/matrix-org/complement/internal/must"
	"github.com/tidwall/gjson"
)

var (
	spaceChildEventType  = "org.matrix.msc1772.space.child"
	spaceParentEventType = "org.matrix.msc1772.space.parent"
)

// the API doesn't return event IDs so we need to key off the
// 3-uple of room ID, event type and state key
func eventKey(srcRoomID, dstRoomID, evType string) string {
	return srcRoomID + "|" + dstRoomID + "|" + evType
}

// Tests that the CS API for MSC2946 works correctly. Creates a space directory like:
//     Root
//      |
// _____|________
// |    |       |
// R1  SS1      R2
//      |________
//      |        |
//     SS2      R3
//      |
//      R4
//
// Where:
// - the user is joined to all rooms except R4.
// - R3 -> SS1 is a parent link without a child.
// - R2 <---> Root is a two-way link.
// - The remaining links are just children links.
// - SS1 is marked as a "space", but SS2 is not.
//
// Tests that:
// - Querying the root returns the entire graph
// - Setting max_rooms_per_space works correctly
// - Setting limit works correctly
// - Rooms are returned correctly along with the custom fields `num_refs` and `room_type`.
// - Events are returned correctly.
// - Redacting links works correctly.
func TestClientSpacesSummary(t *testing.T) {
	deployment := Deploy(t, "msc2946", b.BlueprintOneToOneRoom)
	defer deployment.Destroy(t)

	roomNames := make(map[string]string)

	// create the rooms
	alice := deployment.Client(t, "hs1", "@alice:hs1")
	root := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "Root",
	})
	roomNames[root] = "Root"
	r1 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "R1",
	})
	roomNames[r1] = "R1"
	ss1 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "Sub-Space 1",
		"topic":  "Some topic for sub-space 1",
		"creation_content": map[string]interface{}{
			"org.matrix.msc1772.type": "org.matrix.msc1772.space",
		},
	})
	roomNames[ss1] = "Sub-Space 1"
	r2 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	ss2 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	r3 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	// alice is not joined to R4
	bob := deployment.Client(t, "hs1", "@bob:hs1")
	r4 := bob.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.history_visibility",
				"state_key": "",
				"content": map[string]string{
					"history_visibility": "world_readable",
				},
			},
		},
	})

	// create the links
	rootToR1 := eventKey(root, r1, spaceChildEventType)
	alice.SendEventSynced(t, root, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r1,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	rootToSS1 := eventKey(root, ss1, spaceChildEventType)
	alice.SendEventSynced(t, root, b.Event{
		Type:     spaceChildEventType,
		StateKey: &ss1,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	rootToR2 := eventKey(root, r2, spaceChildEventType)
	alice.SendEventSynced(t, root, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r2,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	r2ToRoot := eventKey(r2, root, spaceParentEventType)
	alice.SendEventSynced(t, r2, b.Event{ // parent link
		Type:     spaceParentEventType,
		StateKey: &root,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	ss1ToSS2 := eventKey(ss1, ss2, spaceChildEventType)
	alice.SendEventSynced(t, ss1, b.Event{
		Type:     spaceChildEventType,
		StateKey: &ss2,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	r3ToSS1 := eventKey(r3, ss1, spaceParentEventType)
	alice.SendEventSynced(t, r3, b.Event{ // parent link only
		Type:     spaceParentEventType,
		StateKey: &ss1,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	ss2ToR4 := eventKey(ss2, r4, spaceChildEventType)
	alice.SendEventSynced(t, ss2, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r4,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})

	// - Querying the root returns the entire graph
	// - Rooms are returned correctly along with the custom fields `num_refs` and `room_type`.
	// - Events are returned correctly.
	t.Run("query whole graph", func(t *testing.T) {
		roomRefs := map[string]int{
			root: 4, // r1,r2,ss1,parent r2
			r1:   1, // root
			r2:   2, // root,parent
			ss1:  3, // root,ss2,r3
			r3:   1, // ss1
			ss2:  2, // ss1,r4
			r4:   1, // ss2
		}
		res := alice.MustDo(t, "POST", []string{"_matrix", "client", "unstable", "rooms", root, "spaces"}, map[string]interface{}{})
		must.MatchResponse(t, res, match.HTTPResponse{
			JSON: []match.JSON{
				match.JSONCheckOff("rooms", []interface{}{
					root, r1, r2, r3, r4, ss1, ss2,
				}, func(r gjson.Result) interface{} {
					return r.Get("room_id").Str
				}, func(roomInt interface{}, data gjson.Result) error {
					roomID := roomInt.(string)
					// check fields
					if name, ok := roomNames[roomID]; ok {
						if data.Get("name").Str != name {
							return fmt.Errorf("room %s got name %s want %s", roomID, data.Get("name").Str, name)
						}
					}
					if refs, ok := roomRefs[roomID]; ok {
						gotRefs := data.Get("num_refs").Int()
						if int64(refs) != gotRefs {
							return fmt.Errorf("room %s got %d refs want %d", roomID, gotRefs, refs)
						}
					}
					if roomID == ss1 {
						wantType := "org.matrix.msc1772.space"
						if data.Get("room_type").Str != wantType {
							return fmt.Errorf("room %s got type %s want %s", roomID, data.Get("room_type").Str, wantType)
						}
					}
					return nil
				}),
				match.JSONCheckOff("events", []interface{}{
					rootToR1, rootToR2, rootToSS1, r2ToRoot,
					ss1ToSS2, r3ToSS1,
					ss2ToR4,
				}, func(r gjson.Result) interface{} {
					return eventKey(r.Get("room_id").Str, r.Get("state_key").Str, r.Get("type").Str)
				}, nil),
			},
		})
	})

	// - Setting max_rooms_per_space works correctly
	t.Run("max_rooms_per_space", func(t *testing.T) {
		// should omit either R1 or R2 if we start from R4 because we only return 1 link per room which will be:
		// R4 -> SS2
		// SS2 -> SS1
		// SS1 -> root
		// root -> R1,R2 (but only 1 is allowed)
		res := alice.MustDo(t, "POST", []string{"_matrix", "client", "unstable", "rooms", r4, "spaces"}, map[string]interface{}{
			"max_rooms_per_space": 1,
		})
		wantItems := []interface{}{
			ss2ToR4, ss1ToSS2, rootToSS1,
			rootToR1, rootToR2, // one of these
		}
		body := must.ParseJSON(t, res.Body)
		gjson.GetBytes(body, "events").ForEach(func(_, val gjson.Result) bool {
			wantItems = must.CheckOff(t, wantItems, eventKey(val.Get("room_id").Str, val.Get("state_key").Str, val.Get("type").Str))
			return true
		})
		if len(wantItems) != 1 {
			if wantItems[0] != rootToR1 && wantItems[0] != rootToR2 {
				t.Errorf("expected fewer events to be returned: %s", string(body))
			}
		}
	})

	// - Setting limit works correctly
	t.Run("limit", func(t *testing.T) {
		// should omit R4 due to limit
		res := alice.MustDo(t, "POST", []string{"_matrix", "client", "unstable", "rooms", root, "spaces"}, map[string]interface{}{
			"limit": 6,
		})
		must.MatchResponse(t, res, match.HTTPResponse{
			JSON: []match.JSON{
				match.JSONCheckOff("rooms", []interface{}{
					root, r1, r2, r3, ss1, ss2,
				}, func(r gjson.Result) interface{} {
					return r.Get("room_id").Str
				}, nil),
				match.JSONCheckOff("events", []interface{}{
					rootToR1, rootToR2, rootToSS1, r2ToRoot,
					ss1ToSS2, r3ToSS1,
				}, func(r gjson.Result) interface{} {
					return eventKey(r.Get("room_id").Str, r.Get("state_key").Str, r.Get("type").Str)
				}, nil),
			},
		})
	})

	t.Run("redact link", func(t *testing.T) {
		// Remove the root -> SS1 link
		alice.SendEventSynced(t, root, b.Event{
			Type:     spaceChildEventType,
			StateKey: &ss1,
			Content:  map[string]interface{}{},
		})
		res := alice.MustDo(t, "POST", []string{"_matrix", "client", "unstable", "rooms", root, "spaces"}, map[string]interface{}{})
		must.MatchResponse(t, res, match.HTTPResponse{
			JSON: []match.JSON{
				match.JSONCheckOff("rooms", []interface{}{
					root, r1, r2,
				}, func(r gjson.Result) interface{} {
					return r.Get("room_id").Str
				}, nil),
				match.JSONCheckOff("events", []interface{}{
					rootToR1, rootToR2, r2ToRoot,
				}, func(r gjson.Result) interface{} {
					return eventKey(r.Get("room_id").Str, r.Get("state_key").Str, r.Get("type").Str)
				}, nil),
			},
		})
	})
}

// Tests that MSC2946 works over federation. Creates a space directory like:
//     ROOT
//      |
// _____|________
// |    |       |
// R1  SS1      r2
//      |________
//      |        |
//     ss2      r3
//      |
//      R4
//
// Where R/SS = on hs1, and r/ss = on hs2. Links are space children state events only.
// Tests that:
// - Querying from root returns the entire graph
func TestFederatedClientSpaces(t *testing.T) {
	deployment := Deploy(t, "msc2946", b.BlueprintFederationOneToOneRoom)
	defer deployment.Destroy(t)

	worldReadable := map[string]interface{}{
		"preset": "public_chat",
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.history_visibility",
				"state_key": "",
				"content": map[string]string{
					"history_visibility": "world_readable",
				},
			},
		},
	}
	// create the rooms
	alice := deployment.Client(t, "hs1", "@alice:hs1")
	root := alice.CreateRoom(t, worldReadable)
	r1 := alice.CreateRoom(t, worldReadable)
	ss1 := alice.CreateRoom(t, worldReadable)
	r4 := alice.CreateRoom(t, worldReadable)
	bob := deployment.Client(t, "hs2", "@bob:hs2")
	r2 := bob.CreateRoom(t, worldReadable)
	ss2 := bob.CreateRoom(t, worldReadable)
	r3 := bob.CreateRoom(t, worldReadable)

	// create the links
	rootToR1 := eventKey(root, r1, spaceChildEventType)
	alice.SendEventSynced(t, root, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r1,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	rootToSS1 := eventKey(root, ss1, spaceChildEventType)
	alice.SendEventSynced(t, root, b.Event{
		Type:     spaceChildEventType,
		StateKey: &ss1,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	rootToR2 := eventKey(root, r2, spaceChildEventType)
	alice.SendEventSynced(t, root, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r2,
		Content: map[string]interface{}{
			"via": []string{"hs2"},
		},
	})
	ss1ToSS2 := eventKey(ss1, ss2, spaceChildEventType)
	alice.SendEventSynced(t, ss1, b.Event{
		Type:     spaceChildEventType,
		StateKey: &ss2,
		Content: map[string]interface{}{
			"via": []string{"hs2"},
		},
	})
	ss1ToR3 := eventKey(ss1, r3, spaceChildEventType)
	alice.SendEventSynced(t, ss1, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r3,
		Content: map[string]interface{}{
			"via": []string{"hs2"},
		},
	})
	ss2ToR4 := eventKey(ss2, r4, spaceChildEventType)
	bob.SendEventSynced(t, ss2, b.Event{
		Type:     spaceChildEventType,
		StateKey: &r4,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	allEvents := []string{
		rootToR1, rootToR2, rootToSS1,
		ss1ToR3, ss1ToSS2,
		ss2ToR4,
	}
	t.Logf("rooms: %v", allEvents)

	res := alice.MustDo(t, "POST", []string{"_matrix", "client", "unstable", "rooms", root, "spaces"}, map[string]interface{}{})
	must.MatchResponse(t, res, match.HTTPResponse{
		JSON: []match.JSON{
			match.JSONCheckOff("rooms", []interface{}{
				root, r1, r2, r3, r4, ss1, ss2,
			}, func(r gjson.Result) interface{} {
				return r.Get("room_id").Str
			}, nil),
		},
	})
}
