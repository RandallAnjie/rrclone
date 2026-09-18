package drive

import "testing"

func TestPublicLinkURL(t *testing.T) {
	const id = "FILEID"
	got := publicLinkURL(id, "video/mp4", false, true)
	want := "https://drive.google.com/uc?export=download&confirm=t&id=FILEID"
	if got != want {
		t.Fatalf("file direct: got %q want %q", got, want)
	}
	got = publicLinkURL(id, "application/vnd.google-apps.document", false, true)
	want = "https://drive.google.com/open?id=FILEID"
	if got != want {
		t.Fatalf("gdoc: got %q want %q", got, want)
	}
	got = publicLinkURL(id, "video/mp4", true, true)
	if got != want {
		t.Fatalf("folder: got %q want %q", got, want)
	}
	got = publicLinkURL(id, "video/mp4", false, false)
	if got != want {
		t.Fatalf("link_direct=false: got %q want %q", got, want)
	}
}
