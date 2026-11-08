package mail

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/emersion/go-imap/v2"
)

const maxExternalHistoryMIMEBytes = 40 << 20
const externalImportUIDWindow = 200

type ExternalHistoryProbe struct {
	UIDValidity uint32
	UpperUID    uint32
}
type ExternalHistoryMessage struct {
	UID     uint32
	Size    int64
	Date    time.Time
	Seen    bool
	Flagged bool
}
type ExternalHistoryRemote interface {
	Inspect(ctx context.Context, provider, username, password, folder string) (ExternalHistoryProbe, error)
	ListWindow(ctx context.Context, provider, username, password, folder string, validity, start, end uint32) ([]ExternalHistoryMessage, error)
	ReadRaw(ctx context.Context, provider, username, password, folder string, validity, uid uint32) ([]byte, error)
}

type liveExternalHistoryRemote struct{}

func (liveExternalHistoryRemote) Inspect(ctx context.Context, provider, username, password, folder string) (ExternalHistoryProbe, error) {
	if !validExternalFolder(folder) {
		return ExternalHistoryProbe{}, errors.New("Invalid source folder")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return ExternalHistoryProbe{}, err
	}
	defer closeClient()
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || selected.UIDValidity == 0 || selected.UIDNext == 0 {
		return ExternalHistoryProbe{}, errors.New("Source folder cannot provide stable UID identity")
	}
	upper := uint32(selected.UIDNext) - 1
	return ExternalHistoryProbe{UIDValidity: selected.UIDValidity, UpperUID: upper}, nil
}
func (liveExternalHistoryRemote) ListWindow(ctx context.Context, provider, username, password, folder string, validity, start, end uint32) ([]ExternalHistoryMessage, error) {
	if !validExternalFolder(folder) || validity == 0 || start == 0 || end < start || end-start >= externalImportUIDWindow {
		return nil, errors.New("Invalid UID window")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || selected.UIDValidity != validity {
		return nil, errors.New("Source UIDVALIDITY changed")
	}
	set := imap.UIDSetNum(imap.UID(start), imap.UID(end))
	rows, err := client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true, InternalDate: true}).Collect()
	if err != nil {
		return nil, errors.New("Cannot read source UID window")
	}
	if len(rows) > externalImportUIDWindow {
		return nil, errors.New("Source UID window overflow")
	}
	out := make([]ExternalHistoryMessage, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.UID == 0 || uint32(row.UID) < start || uint32(row.UID) > end {
			return nil, errors.New("Untrusted UID response")
		}
		msg := ExternalHistoryMessage{UID: uint32(row.UID), Size: row.RFC822Size, Date: row.InternalDate}
		for _, flag := range row.Flags {
			if flag == imap.FlagSeen {
				msg.Seen = true
			}
			if flag == imap.FlagFlagged {
				msg.Flagged = true
			}
		}
		out = append(out, msg)
	}
	// Some servers return descending UID; process monotonic, so a single cursor
	// can be committed after every successful migration item.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].UID > out[j].UID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].UID == out[i].UID {
			return nil, errors.New("Duplicate source UID")
		}
	}
	return out, nil
}
func (liveExternalHistoryRemote) ReadRaw(ctx context.Context, provider, username, password, folder string, validity, uid uint32) ([]byte, error) {
	if !validExternalFolder(folder) || validity == 0 || uid == 0 {
		return nil, errors.New("Invalid source item")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || selected.UIDValidity != validity {
		return nil, errors.New("Source folder UIDVALIDITY changed")
	}
	set := imap.UIDSetNum(imap.UID(uid))
	sizeRows, err := client.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
	if err != nil || len(sizeRows) != 1 || uint32(sizeRows[0].UID) != uid || sizeRows[0].RFC822Size <= 0 || sizeRows[0].RFC822Size > maxExternalHistoryMIMEBytes {
		return nil, errors.New("Source message unavailable or over size limit")
	}
	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: int64(maxExternalHistoryMIMEBytes + 1)}}
	rows, err := client.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil || len(rows) != 1 || uint32(rows[0].UID) != uid {
		return nil, errors.New("Source MIME fetch failed")
	}
	body := rows[0].FindBodySection(section)
	if len(body) == 0 || len(body) > maxExternalHistoryMIMEBytes || int64(len(body)) != sizeRows[0].RFC822Size {
		return nil, errors.New("Source MIME incomplete")
	}
	if len(body) > math.MaxInt32 {
		return nil, errors.New("Source MIME too large")
	}
	return body, nil
}
