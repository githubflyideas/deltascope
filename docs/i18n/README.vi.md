<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Máy hiện sóng phát hiện suy giảm hiệu năng cho một host Linux — kèm nhận định của bác sĩ**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · **Tiếng Việt**


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Xem thêm giao diện ở README tiếng Anh</sub>
</div>

## Nó làm gì

Chọn hai cửa sổ thời gian — **đường cơ sở A** và **nghi vấn B** — deltascope sẽ so sánh trung bình từng chỉ số từ kho lưu trữ lịch sử cục bộ, đánh giá mỗi thay đổi theo cực tính của chỉ số và tạo báo cáo ba tầng: **chẩn đoán → bằng chứng → dữ liệu đầy đủ**.

## Tính năng

- **Bộ máy quy tắc chẩn đoán** — 16 quy tắc liên chỉ số tích hợp (vòng xoáy swap, bão hòa đĩa, tràn hàng đợi accept, OOM, điểm nóng một nhân, áp lực SYN, phát hiện khởi động lại…). Mỗi lần khớp cho ra kết luận dễ hiểu, bằng chứng và các lệnh tiếp theo. Không bao giờ có điểm sức khỏe tổng hợp.
- **146 chỉ số tích hợp, 5 nhóm** — gồm PSI, softnet drop, điểm nóng theo nhân (tự gập), phân bố trạng thái TCP, direct reclaim, LVM/MD. Ngưỡng riêng cho các bộ đếm nhiễu.
- **Báo cáo dữ liệu đầy đủ** — hàng ổn định vẫn hiển thị nhưng mờ đi, thứ tự hàng cố định, độ đậm nền ∝ |Δ|, xuất hiện ⊕ / biến mất ⊖ đánh dấu riêng, neo Top-5.
- **Mọi thứ là tệp cấu hình** — danh mục, quy tắc và ngưỡng là JSON xuất được, kiểm tra khi nạp. `profiles/` có hai bậc full/core.
- **Chế độ headless** — `deltascope compare` in cùng báo cáo dưới dạng văn bản (ANSI) hoặc JSON, mã thoát 2 khi có suy giảm: dùng ngay với cron.
- **Thiết kế cho môi trường cô lập** — một binary tĩnh, UI và biểu đồ nhúng sẵn, xác thực cục bộ, không CDN, không telemetry, không lưu lượng ra ngoài.

## Bắt đầu nhanh

Binary dựng sẵn trong [`dist/`](../../dist/): `linux-amd64` (kernel ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (kernel 2.6.32).

Dựng từ mã nguồn (một lần, máy có internet):

```bash
make vendor && make test && make build
```

Triển khai (tham chiếu Rocky Linux 9, có thể hoàn toàn offline):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
```

## Cách dùng

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## Ngữ nghĩa so sánh

- Bộ đếm được lấy trung bình **theo tốc độ** trên mỗi cửa sổ (ngữ nghĩa pmdiff)
- Δ% = (B − A) / |A| × 100; chỉ đánh giá khi `|Δ| ≥ ngưỡng` (mặc định 15%, chỉ số nhiễu có ngưỡng riêng)
- Cực tính: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 báo là ∞; chỉ số vắng cả hai phía được bỏ qua lặng lẽ
- Xuất hiện ⊕ / biến mất ⊖ là sự kiện hạng nhất

## Bảo mật

PBKDF2-HMAC-SHA256 (600k vòng) · phiên không trạng thái ký HMAC · giới hạn đăng nhập theo IP · whitelist tên chỉ số + exec dạng mảng (không bao giờ qua shell) · CSP nghiêm ngặt không inline · systemd unit gia cố · thông tin đăng nhập không rời khỏi host.

## Ghi chú

Cửa sổ hiểu theo múi giờ cục bộ của máy chủ · tối đa 32 ngày mỗi cửa sổ · bước xu hướng tự thích ứng · biểu đồ đóng gói sẵn (Apache-2.0) · ngày đầu cần chờ kho lưu trữ tích lũy.
