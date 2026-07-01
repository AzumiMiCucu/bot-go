package src

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// RAW NODE / IQ INJECTION — seam paling langka di whatsmeow.
// =================================================================
// whatsmeow tak mengekspos jalur untuk membuat <iq> mentah, TAPI membukanya
// lewat Client.DangerousInternals().SendIQ(query). Berbeda dengan SendGroup
// (yang argumennya bertipe unexported → wajib reflection), tipe query di sini
// DIALIASKAN publik: `whatsmeow.DangerousInfoQuery` (= infoQuery) &
// `whatsmeow.DangerousInfoQueryType` (= infoQueryType). Jadi kita bisa menyusun
// query langsung TANPA reflection.
//
// Gunanya: memanggil fitur/endpoint WA yang BELUM di-wrap whatsmeow (server
// props, ping latency, query privasi, dsb). Ini "SendNode untuk fitur yang
// belum ada API-nya".
// =================================================================

// SendRawIQ mengirim <iq> mentah ke server WA dan mengembalikan node balasannya.
// namespace = xmlns (mis. "w:p", "w", "usync"), iqType = "get"/"set".
// to kosong → default ke server WA (s.whatsapp.net). children = isi node <iq>.
func SendRawIQ(ctx context.Context, client *whatsmeow.Client, namespace, iqType string, to types.JID, children []waBinary.Node) (*waBinary.Node, error) {
	if client == nil {
		return nil, fmt.Errorf("client belum siap")
	}
	if to.IsEmpty() {
		to = types.ServerJID
	}
	q := whatsmeow.DangerousInfoQuery{
		Namespace: namespace,
		Type:      whatsmeow.DangerousInfoQueryType(iqType),
		To:        to,
		Timeout:   15 * time.Second,
	}
	if len(children) > 0 {
		q.Content = children
	}
	return client.DangerousInternals().SendIQ(ctx, q)
}

// PingServer mengukur round-trip nyata ke server WhatsApp memakai IQ w:p/ping —
// fitur protokol asli WA yang TIDAK diekspos whatsmeow. Berbeda dari "ping" bot
// internal: ini benar-benar menyapa server dan menunggu ack-nya.
func PingServer(ctx context.Context, client *whatsmeow.Client) (time.Duration, error) {
	start := time.Now()
	_, err := SendRawIQ(ctx, client, "w:p", "get", types.ServerJID, []waBinary.Node{{Tag: "ping"}})
	if err != nil {
		return 0, err
	}
	return time.Since(start), nil
}
