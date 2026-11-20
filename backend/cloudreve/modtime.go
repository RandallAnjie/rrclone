package cloudreve

// Keeping modification times.
//
// Cloudreve stores the last_modified of an upload as the updated_at of
// the file, but moving, renaming or copying a file resets updated_at to
// the current time and no API sets it. So where updated_at no longer
// tells the truth rclone records the time in the metadata of the file,
// which moves and copies keep.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/cloudreve/api"
	"github.com/rclone/rclone/lib/rest"
)

// modTimeKey is the metadata key of the recorded time. Metadata under
// dav: is free form and writing it leaves updated_at alone, but it is
// read as a WebDAV dead property, so must be <namespace>|<name>.
const modTimeKey = "dav:https://rclone.org/|mtime"

// deadProp is the value of a WebDAV dead property
type deadProp struct {
	InnerXML []byte `json:"i"`
}

// encodeModTime makes the metadata value recording t for the content
// entity. Tying it to the entity makes the record lapse when anything
// else uploads a new version of the file.
func encodeModTime(t time.Time, entity string) (string, error) {
	inner := fmt.Sprintf("%d %d %s", t.Unix(), t.Nanosecond(), entity)
	value, err := json.Marshal(deadProp{InnerXML: []byte(inner)})
	return string(value), err
}

// decodeModTime reads a recorded time, reporting whether it applies to
// the content entity.
func decodeModTime(value, entity string) (t time.Time, ok bool) {
	if value == "" {
		return t, false
	}
	var prop deadProp
	if json.Unmarshal([]byte(value), &prop) != nil {
		return t, false
	}
	fields := strings.Fields(string(prop.InnerXML))
	if len(fields) != 3 || fields[2] != entity {
		return t, false
	}
	sec, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return t, false
	}
	nsec, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return t, false
	}
	return time.Unix(sec, nsec), true
}

// fileModTime returns the modification time of file
func fileModTime(file *api.File) time.Time {
	if t, ok := decodeModTime(file.Metadata[modTimeKey], file.PrimaryEntity); ok {
		return t
	}
	return file.UpdatedAt
}

// patchModTime records t as the modification time of the file at uri
func (f *Fs) patchModTime(ctx context.Context, uri, entity string, t time.Time) error {
	value, err := encodeModTime(t, entity)
	if err != nil {
		return err
	}
	opts := rest.Opts{Method: "PATCH", Path: "/file/metadata"}
	req := &api.PatchMetadataRequest{
		URIs:    []string{uri},
		Patches: []api.MetadataPatch{{Key: modTimeKey, Value: value}},
	}
	return f.call(ctx, f.srv, &opts, req, nil)
}
