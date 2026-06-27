package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	// WAJIB DITAMBAHKAN: Agar Golang bisa membaca dan memotong gambar Jpeg/Png
	_ "image/jpeg"
	_ "image/png"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =============================================
// REGISTRASI SEMUA COMMAND GRUP
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Tambah Member",
		Category:    "Group",
		Aliases:     []string{"add", "tambah"},
		Pattern:     regexp.MustCompile(`(?i)^(?:add|tambah)\s+(.+)`),
		Description: "Menambahkan member massal (khusus admin)",
		Execute:     ExecuteAddMember,
	})
	RegisterCommand(Command{
		Name:        "Keluarkan Member",
		Category:    "Group",
		Aliases:     []string{"kick", "remove", "dor"},
		Pattern:     regexp.MustCompile(`(?i)^(?:kick|dor|remove|tendang)\s+(.+)`),
		Description: "Mengeluarkan member massal (reply/tag, khusus admin)",
		Execute:     ExecuteKickMember,
	})
	RegisterCommand(Command{
		Name:        "Jadikan Admin",
		Category:    "Group",
		Aliases:     []string{"promote", "admin"},
		Pattern:     regexp.MustCompile(`(?i)^(?:promote|admin)\s+(.+)`),
		Description: "Menjadikan member sebagai admin (reply/tag, khusus admin)",
		Execute:     ExecutePromote,
	})
	RegisterCommand(Command{
		Name:        "Copot Admin",
		Category:    "Group",
		Aliases:     []string{"demote", "unadmin"},
		Pattern:     regexp.MustCompile(`(?i)^(?:demote|unadmin)\s+(.+)`),
		Description: "Mencabut jabatan admin (reply/tag, khusus admin)",
		Execute:     ExecuteDemote,
	})
	RegisterCommand(Command{
		Name:        "Pengumuman/Hidetag",
		Category:    "Group",
		Aliases:     []string{"hidetag", "tagall"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(hidetag|tagall|pengumuman)\s*$`),
		Description: "Tag seluruh member grup (khusus admin)",
		Execute:     ExecuteHidetag,
	})
	RegisterCommand(Command{
		Name:        "Link Grup",
		Category:    "Group",
		Aliases:     []string{"linkgrup", "grouplink"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(linkgrup|grouplink)\s*$`),
		Description: "Mendapatkan tautan undangan grup (khusus admin)",
		Execute:     ExecuteGetLink,
	})
	RegisterCommand(Command{
		Name:        "Reset Link Grup",
		Category:    "Group",
		Aliases:     []string{"resetlink", "cabutlink"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(resetlink|cabutlink)\s*$`),
		Description: "Mengganti tautan grup dengan yang baru (khusus admin)",
		Execute:     ExecuteRevokeLink,
	})
	RegisterCommand(Command{
		Name:        "Ganti Nama Grup",
		Category:    "Group",
		Aliases:     []string{"namagrup", "setname"},
		Pattern:     regexp.MustCompile(`(?i)^(?:namagrup|setname)\s+(.+)`),
		Description: "Mengubah nama grup (khusus admin)",
		Execute:     ExecuteSetGroupName,
	})
	RegisterCommand(Command{
		Name:        "Ganti Deskripsi Grup",
		Category:    "Group",
		Aliases:     []string{"deskripsi", "setdesc"},
		Pattern:     regexp.MustCompile(`(?i)^(?:deskripsi|setdesc)\s+([\s\S]+)`),
		Description: "Mengubah deskripsi grup (khusus admin)",
		Execute:     ExecuteSetGroupDesc,
	})
	RegisterCommand(Command{
		Name:        "Ganti Foto Grup",
		Category:    "Group",
		Aliases:     []string{"fotogrup", "setpp"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(fotogrup|setpp)\s*$`),
		Description: "Mengubah foto profil grup (kirim/reply gambar, khusus admin)",
		Execute:     ExecuteSetGroupPhoto,
	})
	RegisterCommand(Command{
		Name:        "Lock Grup",
		Category:    "Group",
		Aliases:     []string{"lock", "kunci", "tutup"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(lock|kunci|tutup)\s*$`),
		Description: "Tutup grup — hanya admin yang bisa kirim pesan (khusus admin)",
		Execute:     ExecuteLockGroup,
	})
	RegisterCommand(Command{
		Name:        "Unlock Grup",
		Category:    "Group",
		Aliases:     []string{"unlock", "buka"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(unlock|buka)\s*$`),
		Description: "Buka grup — semua bisa kirim pesan (khusus admin)",
		Execute:     ExecuteUnlockGroup,
	})
	RegisterCommand(Command{
		Name:        "Info Grup",
		Category:    "Group",
		Aliases:     []string{"infogrup", "grupinfo"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(infogrup|grupinfo)\s*$`),
		Description: "Menampilkan informasi detail grup",
		Execute:     ExecuteGroupInfo,
	})
}

// =============================================
// HELPER CERDAS (SMART EXTRACTION)
// =============================================

// Memastikan perintah ini hanya berjalan di Grup
func isGroupValid(ctx *ContextBot) bool {
	if ctx.ChatJID.Server != "g.us" {
		ctx.Reply("❌ Perintah ini hanya bisa digunakan di dalam Grup!")
		return false
	}
	return true
}

func isUserAdmin(ctx *ContextBot) (bool, error) {
	if ctx.IsOwner {
		return true, nil
	}

	groupInfo, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID)
	if err != nil {
		return false, fmt.Errorf("gagal ambil info grup")
	}
	return userIsAdminIn(ctx, groupInfo), nil
}

// userIsAdminIn memeriksa status admin pengirim memakai GroupInfo yang SUDAH diambil
// (hindari fetch ganda). Owner bot selalu dianggap admin.
func userIsAdminIn(ctx *ContextBot, gi *types.GroupInfo) bool {
	if ctx.IsOwner {
		return true
	}
	senderUser := ctx.Msg.Info.Sender.ToNonAD().User
	senderLID := ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().User
	for _, p := range gi.Participants {
		partUser := p.JID.ToNonAD().User
		partLID := p.LID.ToNonAD().User
		if (partUser != "" && (partUser == senderUser || partUser == senderLID)) ||
			(partLID != "" && (partLID == senderLID || partLID == senderUser)) {
			return p.IsAdmin || p.IsSuperAdmin
		}
	}
	return false
}

// botAdminStatus mengembalikan (ketemu, apakahAdmin) untuk akun BOT di grup.
// "ketemu" false bila partisipan bot tak terdeteksi (mis. grup LID) — pemanggil
// sebaiknya TIDAK memblokir aksi pada kasus ini, biar server WA yang menentukan.
func botAdminStatus(ctx *ContextBot, gi *types.GroupInfo) (found, admin bool) {
	if ctx.Client.Store.ID == nil {
		return false, false
	}
	botUser := ctx.Client.Store.ID.ToNonAD().User
	botLID := ctx.Client.Store.LID.ToNonAD().User
	for _, p := range gi.Participants {
		if (botUser != "" && p.JID.ToNonAD().User == botUser) ||
			(botLID != "" && p.LID.ToNonAD().User == botLID) {
			return true, p.IsAdmin || p.IsSuperAdmin
		}
	}
	return false, false
}

// resolveTargets memetakan tiap target ke JID partisipan yang SEBENARNYA di grup
// (mis. dari mention @lid → nomor telepon), sehingga UpdateGroupParticipants tak
// gagal diam-diam. Target yang tak ditemukan (mis. calon member baru) dibiarkan apa adanya.
func resolveTargets(gi *types.GroupInfo, targets []types.JID) []types.JID {
	out := make([]types.JID, 0, len(targets))
	for _, t := range targets {
		tu := t.ToNonAD().User
		var matched types.JID
		for _, p := range gi.Participants {
			if (p.JID.ToNonAD().User != "" && p.JID.ToNonAD().User == tu) ||
				(p.LID.ToNonAD().User != "" && p.LID.ToNonAD().User == tu) {
				matched = p.JID.ToNonAD()
				break
			}
		}
		if matched.User != "" {
			out = append(out, matched)
		} else {
			out = append(out, t.ToNonAD())
		}
	}
	return out
}

// participantActionReport merangkum hasil per-anggota dari UpdateGroupParticipants
// menjadi (jumlah sukses, baris laporan). Mengubah kode error WA jadi keterangan.
func participantActionReport(resp []types.GroupParticipant) (okCount int, lines []string) {
	for _, r := range resp {
		switch r.Error {
		case 0:
			okCount++
			lines = append(lines, "✅ +"+r.JID.User)
		case 403:
			lines = append(lines, "⛔ +"+r.JID.User+" (privasi/izin ketat)")
		case 404, 408:
			lines = append(lines, "⚠️ +"+r.JID.User+" (tidak valid / tak di grup)")
		case 406:
			lines = append(lines, "🚫 +"+r.JID.User+" (tak bisa diproses, mungkin pembuat grup)")
		case 409:
			lines = append(lines, "ℹ️ +"+r.JID.User+" (status sudah sesuai)")
		default:
			lines = append(lines, fmt.Sprintf("❓ +%s (kode %d)", r.JID.User, r.Error))
		}
	}
	return okCount, lines
}

// runMemberAction menjalankan aksi keanggotaan (remove/promote/demote) dengan
// pemeriksaan lengkap: validasi grup → admin pengirim → bot harus admin →
// resolusi target (LID) → laporan hasil per-anggota. Khusus "remove" melindungi
// bot agar tak menendang dirinya sendiri.
func runMemberAction(ctx *ContextBot, action whatsmeow.ParticipantChange, okTitle string) error {
	if !isGroupValid(ctx) {
		return nil
	}
	gi, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID)
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil info grup.")
	}
	if !userIsAdminIn(ctx, gi) {
		return nil // pengirim bukan admin → diam (hindari spam)
	}
	if found, admin := botAdminStatus(ctx, gi); found && !admin {
		return ctx.Reply("⚠️ Bot harus menjadi *admin* dulu untuk menjalankan perintah ini.")
	}

	targets, err := getTargetJIDs(ctx)
	if err != nil {
		return ctx.Reply(err.Error())
	}
	targets = resolveTargets(gi, targets)

	if action == whatsmeow.ParticipantChangeRemove {
		var safe []types.JID
		botUser := ""
		if ctx.Client.Store.ID != nil {
			botUser = ctx.Client.Store.ID.ToNonAD().User
		}
		for _, j := range targets {
			if j.User != botUser {
				safe = append(safe, j)
			}
		}
		if len(safe) == 0 {
			return ctx.Reply("😅 Bot tidak bisa mengeluarkan dirinya sendiri.")
		}
		targets = safe
	}

	_ = ctx.React("⏳")
	resp, err := ctx.Client.UpdateGroupParticipants(context.Background(), ctx.ChatJID, targets, action)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memproses. (Pastikan bot adalah Admin)\n_" + err.Error() + "_")
	}

	okCount, lines := participantActionReport(resp)
	_ = ctx.React("✅")
	return ctx.Reply(fmt.Sprintf("%s — *%d/%d berhasil*\n\n%s", okTitle, okCount, len(resp), strings.Join(lines, "\n")))
}

// Middleware Utama
func guardAdminAccess(ctx *ContextBot) bool {
	if !isGroupValid(ctx) {
		return false
	}

	userAdmin, err := isUserAdmin(ctx)
	if err != nil || !userAdmin {
		return false
	}

	return true
}

// Ekstraksi Mentions, Reply, atau Nomor Raw
func getTargetJIDs(ctx *ContextBot) ([]types.JID, error) {
	var jids []types.JID
	seen := make(map[string]bool)

	if ext := ctx.Msg.Message.GetExtendedTextMessage(); ext != nil {
		if ctxInfo := ext.GetContextInfo(); ctxInfo != nil {
			for _, m := range ctxInfo.GetMentionedJID() {
				if parsed, err := types.ParseJID(m); err == nil {
					if !seen[parsed.User] {
						jids = append(jids, parsed.ToNonAD())
						seen[parsed.User] = true
					}
				}
			}
		}
	}

	if len(jids) == 0 {
		if ext := ctx.Msg.Message.GetExtendedTextMessage(); ext != nil {
			if ctxInfo := ext.GetContextInfo(); ctxInfo != nil {
				if participant := ctxInfo.GetParticipant(); participant != "" {
					if parsed, err := types.ParseJID(participant); err == nil {
						if !seen[parsed.User] {
							jids = append(jids, parsed.ToNonAD())
							seen[parsed.User] = true
						}
					}
				}
			}
		}
	}

	if len(jids) == 0 && ctx.Args != "" {
		re := regexp.MustCompile(`\+?(\d{7,15})`)
		matches := re.FindAllStringSubmatch(ctx.Args, -1)
		for _, match := range matches {
			if parsed, err := types.ParseJID(match[1] + "@s.whatsapp.net"); err == nil {
				if !seen[parsed.User] {
					jids = append(jids, parsed.ToNonAD())
					seen[parsed.User] = true
				}
			}
		}
	}

	if len(jids) == 0 {
		return nil, fmt.Errorf("⚠️ Format salah!\n\n*Cara penggunaan:*\n› Reply pesan orangnya\n› Tag orangnya (@user)")
	}
	return jids, nil
}

// Helper untuk membaca buffer foto
func getGroupImageBuffer(ctx *ContextBot) ([]byte, error) {
	msg := ctx.Msg.Message

	if img := msg.GetImageMessage(); img != nil {
		return ctx.Client.Download(context.Background(), img)
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		if ctxInfo := ext.GetContextInfo(); ctxInfo != nil {
			if quoted := ctxInfo.GetQuotedMessage(); quoted != nil {
				if img := quoted.GetImageMessage(); img != nil {
					return ctx.Client.Download(context.Background(), img)
				}
			}
		}
	}
	return nil, fmt.Errorf("tidak ada gambar")
}

// =============================================
// COMMAND: MANAJEMEN MEMBER
// =============================================

func ExecuteAddMember(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	targetJIDs, err := getTargetJIDs(ctx)
	if err != nil {
		return ctx.Reply(err.Error())
	}

	_ = ctx.React("⏳")
	resp, err := ctx.Client.UpdateGroupParticipants(context.Background(), ctx.ChatJID, targetJIDs, "add")
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Terjadi kesalahan server. (Pastikan bot adalah Admin)")
	}

	var report []string
	for _, r := range resp {
		switch r.Error {
		case 403:
			report = append(report, fmt.Sprintf("⛔ +%s (Privasi Ketat)", r.JID.User))
		case 408, 400:
			report = append(report, fmt.Sprintf("⚠️ +%s (Tidak valid)", r.JID.User))
		case 409:
			report = append(report, fmt.Sprintf("ℹ️ +%s (Sudah di grup)", r.JID.User))
		case 0:
			report = append(report, fmt.Sprintf("✅ +%s (Sukses)", r.JID.User))
		default:
			report = append(report, fmt.Sprintf("❓ +%s (Kode error: %d)", r.JID.User, r.Error))
		}
	}

	_ = ctx.React("✅")
	return ctx.Reply(fmt.Sprintf("📊 *Laporan Tambah Member:*\n\n%s", strings.Join(report, "\n")))
}

func ExecuteKickMember(ctx *ContextBot) error {
	return runMemberAction(ctx, whatsmeow.ParticipantChangeRemove, "🧹 *Keluarkan Member*")
}

func ExecutePromote(ctx *ContextBot) error {
	return runMemberAction(ctx, whatsmeow.ParticipantChangePromote, "👑 *Jadikan Admin*")
}

func ExecuteDemote(ctx *ContextBot) error {
	return runMemberAction(ctx, whatsmeow.ParticipantChangeDemote, "📉 *Copot Admin*")
}

// =============================================
// COMMAND: PENGUMUMAN & LINK
// =============================================

func ExecuteHidetag(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	groupInfo, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID)
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil daftar member.")
	}

	var mentions []string
	for _, p := range groupInfo.Participants {
		// Grup LID: sebagian partisipan hanya punya LID (JID kosong) → pakai LID
		// agar mention tidak rusak.
		if p.JID.User != "" {
			mentions = append(mentions, p.JID.String())
		} else if p.LID.User != "" {
			mentions = append(mentions, p.LID.String())
		}
	}

	teks := strings.TrimSpace(ctx.Args)
	if teks == "" {
		teks = "📢 *PENGUMUMAN GRUP*\nMohon perhatian dari seluruh member."
	}

	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String(teks),
			ContextInfo: &waProto.ContextInfo{
				MentionedJID: mentions,
			},
		},
	}

	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msg, AndroidExtra())
	return err
}

func ExecuteGetLink(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	inviteCode, err := ctx.Client.GetGroupInviteLink(context.Background(), ctx.ChatJID, false)
	if err != nil {
		return ctx.Reply("❌ Gagal mendapatkan link grup. (Pastikan bot adalah Admin)")
	}

	link := inviteCode
	if !strings.HasPrefix(inviteCode, "http") {
		link = fmt.Sprintf("https://chat.whatsapp.com/%s", inviteCode)
	}

	return ctx.Reply(fmt.Sprintf("🔗 *TAUTAN UNDANGAN GRUP*\n\n%s\n\n_Bagikan link ini untuk mengundang orang masuk._", link))
}

func ExecuteRevokeLink(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	inviteCode, err := ctx.Client.GetGroupInviteLink(context.Background(), ctx.ChatJID, true)
	if err != nil {
		return ctx.Reply("❌ Gagal mereset tautan grup. (Pastikan bot adalah Admin)")
	}

	link := inviteCode
	if !strings.HasPrefix(inviteCode, "http") {
		link = fmt.Sprintf("https://chat.whatsapp.com/%s", inviteCode)
	}

	return ctx.Reply(fmt.Sprintf("🔄 *TAUTAN GRUP BERHASIL DIRESET*\n\nLink lama sudah tidak berlaku lagi.\nLink baru:\n%s", link))
}

// =============================================
// COMMAND: MANAJEMEN INFO GRUP
// =============================================

func ExecuteSetGroupName(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	text := strings.TrimSpace(ctx.TextMessage)
	re := regexp.MustCompile(`(?i)^(?:namagrup|setname)\s+(.+)`)
	matches := re.FindStringSubmatch(text)

	if len(matches) < 2 {
		return ctx.Reply("❌ Format salah.\nContoh: *namagrup Komunitas Keren*")
	}

	newName := strings.TrimSpace(matches[1])

	if len(newName) > 100 {
		return ctx.Reply("❌ Nama grup terlalu panjang (maksimal 100 karakter).")
	}

	err := ctx.Client.SetGroupName(context.Background(), ctx.ChatJID, newName)
	if err != nil {
		return ctx.Reply("❌ Gagal mengganti nama grup. (Pastikan bot adalah Admin)")
	}
	return ctx.Reply(fmt.Sprintf("✅ Nama grup sukses diubah menjadi:\n*%s*", newName))
}

func ExecuteSetGroupDesc(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	text := strings.TrimSpace(ctx.TextMessage)
	re := regexp.MustCompile(`(?i)^(?:deskripsi|setdesc)\s+([\s\S]+)`)
	matches := re.FindStringSubmatch(text)

	if len(matches) < 2 {
		return ctx.Reply("❌ Deskripsi tidak boleh kosong.\nContoh: *setdesc Grup khusus obrolan IT.*")
	}

	newDesc := strings.TrimSpace(matches[1])

	err := ctx.Client.SetGroupDescription(context.Background(), ctx.ChatJID, newDesc)
	if err != nil {
		return ctx.Reply("❌ Gagal mengganti deskripsi grup. (Pastikan bot adalah Admin)")
	}
	return ctx.Reply("✅ Deskripsi grup berhasil diperbarui!")
}

func ExecuteSetGroupPhoto(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}

	imgBuffer, err := getGroupImageBuffer(ctx)
	if err != nil || len(imgBuffer) == 0 {
		return ctx.Reply("❌ *Cara pakai:*\n\nKirim gambar atau reply gambar dengan caption *fotogrup*.")
	}

	pictureID, err := ctx.Client.SetGroupPhoto(context.Background(), ctx.ChatJID, imgBuffer)

	if err != nil {
		fmt.Printf("[DEBUG] Error SetGroupPhoto: %v\n", err)
		return ctx.Reply(fmt.Sprintf("❌ Gagal ganti foto grup: %s\n\n_Pastikan format file mendukung (JPG/PNG)._", err.Error()))
	}

	return ctx.Reply(fmt.Sprintf("🖼️ Foto profil grup berhasil diperbarui! (ID: %s)", pictureID))
}

func ExecuteLockGroup(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}
	err := ctx.Client.SetGroupAnnounce(context.Background(), ctx.ChatJID, true)
	if err != nil {
		return ctx.Reply("❌ Gagal mengunci grup. (Pastikan bot adalah Admin)")
	}
	return ctx.Reply("🔒 *GRUP DIKUNCI*\n\nHanya admin yang bisa mengirim pesan sekarang.")
}

func ExecuteUnlockGroup(ctx *ContextBot) error {
	if !guardAdminAccess(ctx) {
		return nil
	}
	err := ctx.Client.SetGroupAnnounce(context.Background(), ctx.ChatJID, false)
	if err != nil {
		return ctx.Reply("❌ Gagal membuka grup. (Pastikan bot adalah Admin)")
	}
	return ctx.Reply("🔓 *GRUP DIBUKA*\n\nSeluruh member sekarang dapat berinteraksi kembali.")
}

func ExecuteGroupInfo(ctx *ContextBot) error {
	if !isGroupValid(ctx) {
		return nil
	}

	groupInfo, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID)
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil profil grup.")
	}

	adminCount := 0
	for _, p := range groupInfo.Participants {
		if p.IsAdmin || p.IsSuperAdmin {
			adminCount++
		}
	}

	lockStatus := "🔓 Terbuka"
	if groupInfo.IsAnnounce {
		lockStatus = "🔒 Terkunci (Hanya Admin)"
	}

	desc := groupInfo.Topic
	if desc == "" {
		desc = "_Tidak ada deskripsi yang dipasang_"
	}

	createdAt := groupInfo.GroupCreated.Format("02 Jan 2006, 15:04 WIB")

	// FIX: Mengambil nilai "User" dari Struct OwnerPN yang bertipe JID
	creatorJid := groupInfo.OwnerPN.User

	// Fallback jika OwnerPN kosong, ambil dari OwnerJID
	if creatorJid == "" {
		creatorJid = groupInfo.OwnerJID.User
	}

	if creatorJid == "" {
		creatorJid = "Tidak diketahui"
	} else {
		creatorJid = "+" + creatorJid
	}

	text := fmt.Sprintf(
		"🏠 *DETAIL INFORMASI GRUP*\n\n"+
			"📌 *Nama:* %s\n"+
			"🆔 *ID Grup:* %s\n"+
			"👑 *Pembuat:* %s\n"+
			"📅 *Dibuat:* %s\n\n"+
			"👥 *Member:* %d Anggota\n"+
			"👮 *Admin:* %d Anggota\n"+
			"🛡️ *Pengaturan:* %s\n\n"+
			"📝 *Deskripsi:*\n%s",
		groupInfo.Name,
		ctx.ChatJID.User,
		creatorJid,
		createdAt,
		len(groupInfo.Participants),
		adminCount,
		lockStatus,
		desc,
	)

	return ctx.Reply(text)
}
