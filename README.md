# RSISA Print Server

Service lokal untuk menghubungkan aplikasi RSISA dengan printer yang tersedia di komputer pengguna. Source proyek ini berasal dari [Aquila Printers Discover](https://github.com/karya-anakbangsa/aquila-printers-discover), dengan fokus repo ini pada layanan print server RSISA.

## Dukungan printer

- **Windows Print Spooler (Winspool):** membaca antrean printer Windows dan mengirim PDF melalui driver yang terpasang.
- **CUPS:** membaca antrean printer Linux/macOS dan mengirim PDF melalui antrean OS.
- **IPP/IPPS:** menemukan printer jaringan yang mengiklankan IPP/IPPS melalui Bonjour/mDNS, membaca kapabilitasnya, dan mengirim PDF langsung jika printer mendukung `application/pdf`.
- **Antrean Windows jarak jauh:** memilih antrean Windows yang ditemukan dan mencetak PDF melalui driver Windows.
- **Tes printer:** mencetak halaman uji melalui antrean printer lokal.

PDF dikirim melalui driver OS atau IPP yang mendukung PDF. Server tidak mengirim PDF mentah ke TCP port 9100.

## Menjalankan

Perlu Go 1.26 atau lebih baru. Dari root repository:

```sh
go run ./cmd/print-server
```

Secara default service hanya mendengarkan di `127.0.0.1:17891`. Periksa statusnya di:

```text
http://127.0.0.1:17891/health
```

Atur origin browser yang diizinkan pada `print-server.json`:

```json
{
  "listen": "127.0.0.1:17891",
  "allowed_origins": ["https://app.example.org"]
}
```

Jalankan dengan konfigurasi di lokasi lain menggunakan `RSISA_PRINT_SERVER_CONFIG=/path/print-server.json`. Pastikan alamat dengar tetap loopback agar service hanya dapat diakses dari komputer yang sama.

## API

Semua endpoint menggunakan alamat lokal `http://127.0.0.1:17891`.

| Method | Path | Fungsi |
| --- | --- | --- |
| `GET` | `/health` | Status service. |
| `GET` | `/printers` | Daftar antrean printer OS dan informasi statusnya. |
| `GET` | `/printers/discover` | Menemukan printer jaringan IPP/IPPS melalui Bonjour/mDNS. |
| `POST` | `/printers/test` | Mencetak halaman uji pada antrean yang dipilih. |
| `POST` | `/print/{document_type}?module_code={module}` | Mengirim dokumen PDF ke tujuan cetak yang dikonfigurasi aplikasi. Gunakan `Content-Type: application/pdf`. |

Contoh mengirim PDF:

```sh
curl -X POST 'http://127.0.0.1:17891/print/etiket_obat?module_code=resep_rajal' \
  -H 'Content-Type: application/pdf' --data-binary @etiket.pdf
```

## Build

```sh
go build -o rsisa-print-server ./cmd/print-server
```

Untuk menjalankan aplikasi desktop Aquila, lihat petunjuk di [`desktop/`](desktop/).
