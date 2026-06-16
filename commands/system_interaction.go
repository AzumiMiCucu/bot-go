package commands

// =================================================================
// Interaksi numerik/navigasi kini ditangani oleh reply_router.go
// (RouteReply) berdasarkan ID pesan bot yang di-reply, BUKAN lagi
// command global `^(\d+|before|after|next|prev)$` yang dulu mencatat
// setiap angka ke command_stats dan terkunci per-user.
//
// File ini sengaja dikosongkan; lihat reply_router.go.
// =================================================================
