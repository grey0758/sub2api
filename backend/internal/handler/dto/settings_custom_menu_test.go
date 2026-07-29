package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCustomMenuItems_PreservesForwardContextTriState(t *testing.T) {
	t.Parallel()

	items := ParseCustomMenuItems(`[
		{"id":"legacy","label":"Legacy","url":"https://example.com/legacy","visibility":"user","sort_order":0},
		{"id":"safe","label":"Safe","url":"https://example.com/safe","visibility":"admin","sort_order":1,"forward_context":false},
		{"id":"trusted","label":"Trusted","url":"https://example.com/trusted","visibility":"user","sort_order":2,"forward_context":true}
	]`)

	require.Len(t, items, 3)
	require.Nil(t, items[0].ForwardContext)
	require.NotNil(t, items[1].ForwardContext)
	require.False(t, *items[1].ForwardContext)
	require.NotNil(t, items[2].ForwardContext)
	require.True(t, *items[2].ForwardContext)

	raw, err := json.Marshal(items)
	require.NoError(t, err)

	var encoded []map[string]any
	require.NoError(t, json.Unmarshal(raw, &encoded))
	require.NotContains(t, encoded[0], "forward_context")
	require.Equal(t, false, encoded[1]["forward_context"])
	require.Equal(t, true, encoded[2]["forward_context"])
}
