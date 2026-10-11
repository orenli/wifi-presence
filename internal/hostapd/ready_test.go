package hostapd

import (
	"context"
	"errors"
	"path"
	"testing"
	"time"

	"github.com/awilliams/wifi-presence/internal/hostapd/hostapdtest"
)

func TestAttachSnapshotBeforeQueuedEvents(t *testing.T) {
	server, err := hostapdtest.NewHostAPD(path.Join(t.TempDir(), "hap"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := hostapdtest.DefaultHostAPDHandler(hostapdtest.StatusResp{}, nil)
	events := make(chan string, 1)
	events <- "AP-STA-CONNECTED 02:00:00:00:00:01"
	handler.OnAttach(func() <-chan string { return events })
	go server.Serve(handler)
	client, err := NewClient(t.TempDir(), server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	finished := errors.New("event processed")
	ready := false
	err = client.AttachWithReady(ctx, func(Event) error {
		if !ready {
			return errors.New("event ran before startup snapshot")
		}
		return finished
	}, func() error {
		// Querying the original command socket must remain possible while the
		// separate ATTACH socket buffers events.
		if _, err := client.Stations(); err != nil {
			return err
		}
		ready = true
		return nil
	})
	if !errors.Is(err, finished) {
		t.Fatalf("AttachWithReady: %v", err)
	}
}
