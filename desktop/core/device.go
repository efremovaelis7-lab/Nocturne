package core

import "encoding/json"

// deviceKeys are parts of library.json that belong to one device rather than
// to the books: the music on this computer or phone and the screen layout.
// Moving the library to another device (an archive without music, or cloud
// sync) leaves them as they are on the receiving device.
var deviceKeys = []string{
	"musicTracks", "musicPlaylists", "activeMusicPlaylistId", "musicVolume", "musicShuffleEnabled",
	"uiFont", "ncZen", "libraryView",
}

// keepDeviceSettings returns incoming with deviceKeys taken from current.
func keepDeviceSettings(incoming, current []byte) ([]byte, bool) {
	var in, cur map[string]json.RawMessage
	if json.Unmarshal(incoming, &in) != nil || json.Unmarshal(current, &cur) != nil {
		return nil, false
	}
	for _, k := range deviceKeys {
		if v, ok := cur[k]; ok {
			in[k] = v
		} else {
			delete(in, k)
		}
	}
	out, err := json.Marshal(in)
	if err != nil || !validLibrary(out) {
		return nil, false
	}
	return out, true
}
