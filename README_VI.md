# Kiro-Go

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Chuyển tài khoản Kiro thành dịch vụ API tương thích Anthropic / OpenAI. Bản fork này của [Quorinex/Kiro-Go](https://github.com/Quorinex/Kiro-Go) (MIT) được hoàn thiện trên lưu lượng thực tế tại [xclis.ai](https://xclis.ai): nhiều tài khoản, nhiều IP ra ngoài, hội thoại dài và đủ loại client bên thứ ba, với mức dùng khớp với số Kiro thực sự tính phí.

[English](README.md) | [中文](README_CN.md) | Tiếng Việt

Nếu dự án này hữu ích với bạn, một Star sẽ rất có ý nghĩa.

## Bạn có gặp những vấn đề này?

- Mỗi lượt của cùng một hội thoại lại chạy trên một tài khoản khác, cache của Kiro không bao giờ trúng và credits hao rất nhanh?
- WorkBuddy, Codex hay các SDK đi qua gateway mà hóa đơn chẳng bao giờ có cache hit?
- Lượt effort=max bị cắt ở mốc 10 phút, hoặc suy luận mười phút rồi không trả lời gì mà vẫn bị trừ credits?
- Hội thoại dài có ảnh chụp màn hình vượt 900 KB là mô hình đột nhiên "quên" hết?
- Muốn mỗi tài khoản dùng IP ra ngoài riêng, kể cả lúc đăng nhập?
- Tài khoản nhiều lên thì trên bảng quản trị không còn phân biệt được tài khoản nào dùng proxy nào, tài khoản nào sắp hết credits?

Bản fork này đã sửa tất cả những điều đó.

## Khác gì so với bản gốc

### Tiết kiệm: cache và mức dùng khớp với cách Kiro tính phí

| Vấn đề | Bản gốc | Bản fork này |
|---|---|---|
| Hội thoại nhảy giữa các tài khoản, mất cache tiền tố của Kiro | Mỗi request xoay sang tài khoản kế tiếp | Giữ mỗi hội thoại trên một tài khoản; ba chế độ điều phối sticky / round_robin / priority và độ ưu tiên từng tài khoản. Đo trên một hội thoại: byte đầu tiên 12.2 s → 3.9 s, credits mỗi lượt 3.46 → khoảng 1.1 |
| Client không thấy cache hit, trả giá đầy đủ | Chỉ mô phỏng khi request có `cache_control`, dựa trên tin nhắn của client | Mô phỏng prompt cache của Anthropic theo đúng nội dung gửi tới Kiro (sau chuyển đổi và cắt bớt), nên cache read / creation bám sát phí Kiro tính; cache ngầm cho client bên thứ ba không gửi `cache_control`; tách theo phiên / API key / tài khoản và giữ qua các lần khởi động lại |
| Đếm mức dùng sai | Suy luận không tính vào output; web_search chỉ tính vòng cuối | Suy luận tính vào output; mỗi vòng web_search tính cả input và output; stream lỗi trước khi có output thì không tính phí |
| Lượt dùng công cụ bị trả tiền hai lần | Lượt công cụ trước bị làm phẳng thành văn bản, cache đứt ở đó | Tùy chọn giữ nguyên cấu trúc lịch sử công cụ để các lượt sau đọc được cache |

### Ổn định: suy luận dài và hội thoại dài không còn bị ngắt

- **Suy luận dài:** timeout nhàn rỗi 2 phút thay cho giới hạn tổng cố định; effort=max được gửi thành xhigh; lượt Kiro đã tính phí nhưng chỉ có suy luận sẽ kết thúc bằng `max_tokens` và được tính phí, thay vì trả 503 khiến client thử lại mãi.
- **Ngữ cảnh lớn:** giới hạn kích thước request có thể chỉnh (mặc định 2 MB, trước đây cố định 900 KB); không còn bỏ lịch sử khi riêng tin nhắn hiện tại đã vượt giới hạn; điểm cắt giữ ổn định giữa các lượt để không liên tục phá cache; ảnh trùng được loại bỏ và ảnh lớn có thể thu nhỏ.
- **Chuyển đổi dự phòng:** phân biệt lỗi phía client, giới hạn tốc độ, hết hạn mức, thông tin đăng nhập sai và lỗi tạm thời, nên không phạt nhầm tài khoản, và client nhận đúng mã 400 / 429 / 503.
- **Streaming:** gửi SSE ping trong lúc mô hình im lặng sinh nội dung; stream bị cắt được báo là lỗi, không bao giờ giả làm kết thúc bình thường.

### Nhiều IP ra ngoài: mỗi tài khoản có thể dùng proxy riêng

- Lưu nhiều proxy (SOCKS5 / HTTP), đặt tên và kiểm tra kết nối bằng một cú nhấp.
- Chọn proxy khi thêm tài khoản: request đăng nhập và lấy token đi ra từ IP đó, các lần làm mới và hội thoại sau cũng vậy.
- Sửa địa chỉ proxy thì các tài khoản gắn với nó tự cập nhật theo.

### Bảng quản trị

- Tài khoản được nhóm theo IP ra ngoài; kéo thả để sắp xếp nhóm; nhóm không còn tài khoản dùng được sẽ tự thu gọn.
- Kéo thả thẻ để đặt độ ưu tiên điều phối; tài khoản mới thêm tự đứng đầu nhóm của nó.
- Thẻ lớn / vừa / nhỏ; tài khoản hết credits hoặc bị tắt hiển thị màu xám, tài khoản bị tắt nhưng còn credits vẫn giữ thanh hạn mức có màu; đếm ngược ngày đặt lại hạn mức.
- Các công tắc tương thích áp dụng ngay từ bảng quản trị, không cần khởi động lại.

### Hiệu năng và tương thích

- Ghi cấu hình theo lô thay vì mỗi request (khoảng 1.5–7 ms → khoảng 41 ns mỗi request); thân request chỉ mã hóa một lần cho mỗi endpoint; byte đầu tiên nhanh hơn.
- Hỗ trợ fable / mythos và ngữ cảnh 1M; xử lý đúng lượt system trong messages và `temperature: 0`; ký các khối thinking; loại bỏ XML tool_use rò rỉ vào câu trả lời; không nhầm `<thinking>` nằm trong trích dẫn hay khối code; có thể đổi "Kiro" trong câu trả lời thành "Claude".

Mô tả đầy đủ các công tắc có trong tài liệu tiếng Anh: [Compatibility and Prompt-Cache Settings](README.md#compatibility-and-prompt-cache-settings). Thay đổi theo từng phiên bản: [CHANGELOG.md](CHANGELOG.md).

## Tính năng

- Anthropic `/v1/messages`, OpenAI `/v1/chat/completions` & OpenAI `/v1/responses`
- Pool nhiều tài khoản: giữ hội thoại trên một tài khoản, ba chế độ điều phối, độ ưu tiên từng tài khoản, nhóm theo IP ra ngoài
- Tự làm mới token, SSE streaming, bảng quản trị Web
- Nhiều cách xác thực: AWS Builder ID, IAM Identity Center (Enterprise SSO), Microsoft Enterprise SSO, SSO Token, cache cục bộ, credentials JSON, Kiro API Key
- Mô phỏng prompt cache của Anthropic, mức dùng khớp với phí Kiro tính
- Theo dõi mức dùng, nhập/xuất tài khoản, đa ngôn ngữ (CN / EN / VI)
- Nhiều proxy ra ngoài (SOCKS5 / HTTP), mỗi tài khoản có thể dùng proxy riêng

## Bắt đầu nhanh

### Docker Compose (Khuyến nghị)

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker Run

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
docker build -t kiro-go .
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  kiro-go
```

> `ghcr.io/quorinex/kiro-go` là image của bản gốc và không có các thay đổi của bản fork này.

### Build từ mã nguồn

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

### Triển khai trên Zeabur

Repo đã có `Dockerfile`, nên có thể build và chạy trên Zeabur ngay.

**Cách 1: Dashboard (một cú nhấp)**

1. Fork repo này về tài khoản GitHub của bạn.
2. Trên Zeabur, tạo service mới, chọn **Deploy from GitHub**, rồi chọn fork của bạn.
3. Zeabur tự nhận `Dockerfile` và build image.
4. Trong tab **Networking**, mở cổng `8080` và gắn domain.
5. Trong tab **Variables**, đặt ít nhất `ADMIN_PASSWORD` (mật khẩu bảng quản trị).
6. Mount Volume tại `/app/data` nếu muốn tài khoản / cấu hình tồn tại sau khi redeploy.

**Cách 2: CLI**

```bash
npm i -g zeabur
zeabur auth login
zeabur deploy
```

> Chạy lệnh từ thư mục gốc dự án. CLI sẽ ghi `.zeabur/context.json` để nhớ project / service đích — file này chứa ID cá nhân, đừng commit.

Khi service đã chạy, mở `https://<your-domain>/admin` để đăng nhập.

Cấu hình được tạo tự động tại `data/config.json`. Mount `/app/data` để lưu bền. Mật khẩu quản trị mặc định là `changeme` — hãy ghi đè bằng biến môi trường `ADMIN_PASSWORD` hoặc đổi trong bảng quản trị trước khi đưa vào production.

## Cách dùng

Mở `http://localhost:8080/admin`, đăng nhập, thêm tài khoản, rồi gọi API:

```bash
# Claude
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"Xin chào!"}]}'

# OpenAI
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Xin chào!"}]}'
```

### Thêm tài khoản Kiro API Key

Trong bảng quản trị, chọn **API Key** khi thêm tài khoản và dán `ksk_...` (hoặc `ksk_...|region`).

Bạn cũng có thể import qua API credentials:

```bash
curl -X POST http://localhost:8080/admin/api/auth/credentials \
  -H "Content-Type: application/json" \
  -H "Cookie: <admin-session>" \
  -d '{"kiroApiKey":"ksk_your_key|us-east-1","authMethod":"api_key","nickname":"cli-key"}'
```

Tài khoản API Key gọi Kiro CLI runtime (`https://runtime.{region}.kiro.dev/`) với `tokentype: API_KEY`. Chúng bỏ qua OAuth refresh và không dùng `profileArn`.

## Chế độ Thinking

Thêm hậu tố (mặc định `-thinking`) vào tên mô hình, ví dụ `claude-sonnet-4.5-thinking`. Các yêu cầu tương thích Claude có cấu hình `thinking` ở top-level như `{"type":"enabled","budget_tokens":2048}` hoặc `{"type":"adaptive"}` cũng tự bật chế độ thinking. Cấu hình định dạng đầu ra trong bảng quản trị tại Cài đặt - Chế độ Thinking.

## Proxy ra ngoài

Với người dùng ở khu vực mạng bị hạn chế, cấu hình proxy ra ngoài trong bảng quản trị tại **Cài đặt - Cài đặt proxy ra ngoài**. Hỗ trợ SOCKS5 và HTTP.

Cài đặt có hiệu lực ngay, không cần khởi động lại.

Khi không kết nối được proxy, các tài khoản dùng proxy đó sẽ bị bỏ qua và yêu cầu chuyển sang tài khoản khác; proxy được đánh dấu "Không khả dụng" trong cài đặt và trên thẻ tài khoản, được thăm dò lại mỗi 30 giây và tự dùng lại khi hoạt động. Có thể chỉnh bằng `upstreamConnectTimeoutSeconds` và `proxyDownSeconds`.

## Biến môi trường

| Biến | Mô tả | Mặc định |
|------|-------|----------|
| `CONFIG_PATH` | Đường dẫn file cấu hình | `data/config.json` |
| `ADMIN_PASSWORD` | Mật khẩu bảng quản trị (ghi đè config) | - |

## Đóng góp

Hoan nghênh thảo luận thân thiện. Nếu gặp vấn đề, hãy thử nhờ Claude Code, Codex hoặc công cụ tương tự trước — hầu hết vấn đề có thể tự giải quyết. PR càng tốt.

## Nhà tài trợ

- [xclis.ai — X全球中转](https://xclis.ai)

## Tuyên bố miễn trừ

Chỉ dùng cho mục đích học tập và nghiên cứu. Không liên kết với Amazon, AWS hay Kiro. Người dùng tự chịu trách nhiệm tuân thủ điều khoản dịch vụ và luật áp dụng. Sử dụng với rủi ro của riêng bạn.

## Giấy phép

[MIT](LICENSE)
