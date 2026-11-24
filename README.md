# Roadster API

یک API مستقل با Go برای ذخیره و همگام‌سازی پیشرفت [Roadster](https://github.com/abelmiri/roadster-game). بازی فعلی همچنان محاسبهٔ سکه، XP و خرید را انجام می‌دهد؛ این سرویس دادهٔ بازیکن را با Passkey به یک حساب متصل می‌کند. برای جدول رتبه‌بندی رقابتی یا اقتصاد مقاوم در برابر تقلب، محاسبهٔ پاداش و خرید باید در آینده به سرور منتقل شود.

**راهنمای کامل اتصال فرانت‌اند:** [docs/frontend-integration.md](docs/frontend-integration.md)؛ شامل نمونهٔ TypeScript، مسیرهای حساب، جریان Passkey، انتقال ذخیرهٔ محلی، کد سکه، تلگرام و خطاها. آدرس سرویس منتشرشده را در `VITE_ROADSTER_API_URL` فرانت‌اند تنظیم کنید.

## پیش‌نیاز و اجرای محلی

- Go 1.27.3 یا نسخهٔ مشخص‌شده در `go.mod`. نصب رسمی: <https://go.dev/dl/>. روی این پروژه نسخهٔ 1.27.3 آزمایش شده است.
- PostgreSQL و ابزار `psql`. برای Render می‌توان از پایگاه دادهٔ PostgreSQL موجود مانند Neon با connection string دارای pooling و TLS استفاده کرد.
- مرورگر با پشتیبانی Passkey و یک origin ثابت برای بازی. برای آزمایش محلی `http://localhost:5173` و `RP_ID=localhost` به‌کار می‌رود؛ در Production باید origin واقعی بازی و نام دامنهٔ آن تنظیم شود.

در macOS با Homebrew، PostgreSQL 18 نصب شده است. اگر می‌خواهید پایگاه دادهٔ محلی دائمی بسازید، این دستورها را یک بار اجرا کنید:

```sh
export PATH="$(brew --prefix postgresql@18)/bin:$PATH"
brew services start postgresql@18
createdb roadster
export DATABASE_URL="postgres://$(whoami)@localhost:5432/roadster?sslmode=disable"
```

```sh
go mod download
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_init.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/002_promos_telegram.sql
GAME_ORIGIN='http://localhost:5173' RP_ID='localhost' go run .
```

متغیرهای لازم در [.env.example](.env.example) فهرست شده‌اند. فایل `.env` به‌صورت خودکار خوانده نمی‌شود؛ متغیرها را در shell یا تنظیمات Render قرار دهید. migration را **یک بار** روی هر پایگاه داده اجرا کنید؛ سرور در زمان درخواست جدول نمی‌سازد.

### کد سکه

مدیر کدها را فقط از CLI و با دسترسی به `DATABASE_URL` می‌سازد؛ هیچ مسیر عمومی برای ساخت کد وجود ندارد. هر کد مقدار سکه، سقف کل مصرف و تاریخ انقضا دارد؛ هر حساب یک بار می‌تواند آن را ثبت کند. کد خام فقط هنگام ساخت چاپ می‌شود و پایگاه داده هش آن را نگه می‌دارد.

```sh
DATABASE_URL="$DATABASE_URL" go run ./cmd/create-code \
  -coins 500 -max-redemptions 100 -expires 2027-01-01T00:00:00Z
```

خروجی را به‌صورت امن به بازیکنان موردنظر بدهید. API مصرف: `POST /v1/me/redeem` با Bearer و `{"code":"RDST-..."}`. برای حساب تازه ابتدا یک ذخیرهٔ نسخهٔ ۱ با `PUT /v1/me/progress` ارسال شود. سرور اعتبار کد را در تراکنش به سکه اضافه می‌کند؛ بازنگری پیشرفت نیز تغییر می‌کند.

اگر می‌خواهید یک کد دلخواه را از **Neon → SQL Editor** بسازید، مقدار کد را در ستون `id` وارد نکنید: `id` شناسهٔ UUID داخلی است و متن کد فقط به‌صورت هش SHA-256 در `code_hash` نگهداری می‌شود. نمونهٔ زیر کدی خصوصی با ۵۰۰ سکه، سقف ۱۰۰ بازیکن و اعتبار ۳۰ روز می‌سازد؛ پیش از اجرا متن کد و این سه مقدار را به مقدار دلخواه خود تغییر دهید. کد فعال را در مخزن Git منتشر نکنید:

```sql
INSERT INTO promo_codes (id, code_hash, coins, max_redemptions, expires_at)
VALUES (
  gen_random_uuid(),
  sha256(convert_to(upper(trim('YOUR-PRIVATE-CODE')), 'UTF8')),
  500,
  100,
  now() + interval '30 days'
)
RETURNING id, coins, max_redemptions, expires_at;
```

برای ثبت، بازیکن متن همان کد خصوصی را در بازی وارد می‌کند؛ حروف کوچک و بزرگ یکسان محسوب می‌شوند. ستون `redeemed_count` را وارد نکنید تا با مقدار پیش‌فرض صفر ساخته شود. اگر اجرای دوبارهٔ دستور با خطای یکتایی `code_hash` مواجه شد، آن کد قبلاً ساخته شده است. این SQL را روی پایگاه دادهٔ متصل به پروژهٔ Production اجرا کنید، نه در ویرایشگر متغیرهای محیطی Render. تابع [`sha256(bytea)` داخلی PostgreSQL](https://www.postgresql.org/docs/current/functions-binarystring.html) هش موردنیاز را تولید می‌کند.

### بات تلگرام

بات را با [@BotFather](https://t.me/BotFather) بسازید و نام کاربری و توکن آن را دریافت کنید. در محیط Production، `TELEGRAM_BOT_TOKEN`، `TELEGRAM_BOT_USERNAME` (بدون `@`) و `TELEGRAM_WEBHOOK_SECRET` را در Render تنظیم کنید. برای secret یک رشتهٔ تصادفی دست‌کم ۱۶ کاراکتری از حروف/عدد/`_`/`-` بسازید؛ نمونهٔ تولید: `openssl rand -hex 32`. این مقادیر را commit نکنید. پس از انتشار نسخه‌ای که این تنظیمات را دارد، webhook را ثبت کنید:

```sh
export TELEGRAM_BOT_TOKEN='...'
export TELEGRAM_WEBHOOK_SECRET='...'
go run ./cmd/set-webhook -api https://YOUR-SERVICE.onrender.com
```

این دستور طبق [Bot API رسمی Telegram](https://core.telegram.org/bots/api#setwebhook) یک webhook HTTPS با هدر secret ثبت می‌کند. بات فقط پیام خصوصی را پردازش می‌کند. بازیکن در بازی `POST /v1/me/telegram/link` را می‌زند، لینک ۱۰ دقیقه‌ای `t.me` را باز می‌کند و Start را می‌زند. دستورهای `/balance`، `/record` و `/redeem CODE` فعال‌اند. `GET /v1/me/telegram` وضعیت اتصال و `DELETE /v1/me/telegram` قطع اتصال را فراهم می‌کند. لینک جدید، لینک قبلی را باطل می‌کند. پیام‌های تکراری Telegram اثر مالی تکراری ندارند.

برای آزمایش دستی Passkey، در ترمینال دیگر اجرا کنید:

```sh
python3 -m http.server 5173 --directory examples
```

سپس `http://localhost:5173/passkey.html` را باز کنید، یک Passkey بسازید، دوباره وارد شوید و پیشرفت را بخوانید. Passkey آزمایشی `localhost` برای دامنهٔ Production قابل استفاده نیست.

## قرارداد API

### WebSocket مسابقه

`GET /v1/game/socket` اتصال WebSocket را برقرار می‌کند. مرورگر پس از اتصال پیام `identify` با نام راننده می‌فرستد و پیام‌های `presence` بازیکنان آزاد، `invitation` دعوت، `room` وضعیت اتاق و `drive` وضعیت رانندگی دیگر اعضا را دریافت می‌کند. پیام‌های ورودی `invite`، `accept`، `decline`، `start`، `rematch` و `leave` گردش کار مسابقه را کنترل می‌کنند. این مسیر به حساب Passkey نیاز ندارد.

همهٔ درخواست‌ها و پاسخ‌ها JSON هستند. خطاها به شکل `{"error":"code"}` برمی‌گردند. مسیرهای پیشرفت و دور بازی به `Authorization: Bearer <token>` نیاز دارند. توکن تصادفی ۳۰ روز اعتبار دارد؛ پایگاه داده فقط هش آن را نگه می‌دارد. `POST /v1/auth/logout` نشست جاری را باطل می‌کند. `GET /health` اتصال پایگاه داده را بررسی می‌کند.

### Passkey

بازیکن، یک شناسهٔ نصب مرورگر را با `crypto.randomUUID()` تولید و در مرورگر ذخیره می‌کند. این شناسه، شناسهٔ سخت‌افزاری دستگاه یا مدرک ورود نیست.

1. `POST /v1/auth/register/options` با `{"deviceId":"<uuid>"}` → `{"ceremonyId":"<uuid>","options":{"publicKey":...}}`
2. در origin بازی، `navigator.credentials.create({publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(options.publicKey)})` را اجرا کنید.
3. `POST /v1/auth/register/verify` با `{"ceremonyId":"<uuid>","credential":<credential.toJSON()>}` → `{"playerId":"<uuid>","token":"...","expiresIn":2592000}`
4. برای ورود بعدی یا دستگاه دوم، همین جریان را با `login/options`، `navigator.credentials.get` و `login/verify` انجام دهید. در مرحلهٔ ورود از `PublicKeyCredential.parseRequestOptionsFromJSON` استفاده کنید.

چالش پنج دقیقه اعتبار دارد و پس از نخستین تلاش تأیید مصرف می‌شود. Passkey قابل همگام‌سازی ممکن است روی دستگاه دوم در دسترس باشد؛ بدون دسترسی به Passkey، بازیابی مستقل حساب در این نسخه وجود ندارد. دادهٔ فنی ورود شامل شناسهٔ حساب، شناسهٔ دستگاه، شناسه و کلید عمومی Passkey و هش نشست است؛ ایمیل ذخیره نمی‌شود.

### پیشرفت و دور بازی

- `GET /v1/me/progress` → `{"revision":0,"progress":null,"xp":0}` برای حساب تازه، یا آخرین ذخیره.
- `PUT /v1/me/progress` → بدنهٔ زیر؛ پاسخ موفق `{"revision":<شماره جدید>}` است. `progress` همان شیء JSON کلید `roadster-progress` با `version:1` است و `xp` از `roadster-xp` می‌آید. `runId` و `runStatus` هنگام ذخیرهٔ یک دور فعال یا پایان‌یافته فرستاده می‌شوند و برای خرید بیرون از دور قابل حذف‌اند.

```json
{
  "revision": 2,
  "progress": {
    "version": 1,
    "coins": 175,
    "score": 920,
    "playerName": "Ada",
    "record": { "distance": 1200, "name": "Ada", "needsName": false },
    "ownedWeapons": ["machine-gun"],
    "weaponUpgrades": {},
    "carUpgrades": {},
    "ownedPaints": ["white"],
    "selectedPaint": "white",
    "selectedVehicle": "roadster",
    "ownedVehicles": ["roadster"],
    "vehiclePaints": {},
    "missions": {}
  },
  "xp": 200,
  "runId": "<uuid>",
  "runStatus": "active"
}
```

- `POST /v1/runs` با `{"idempotencyKey":"<uuid>"}` → `{"runId":"<uuid>","status":"active","revision":<شماره جدید>}`. برای هر تلاش شروع دور یک کلید جدید بسازید؛ برای retry همان کلید را بفرستید. دور تازه امتیاز آخرین دور را صفر می‌کند و بقیهٔ پیشرفت را حفظ می‌کند. دور فعال پیشین `abandoned` می‌شود.
- `PUT /v1/me/progress` با `runStatus:"finished"` پایان دور را ثبت می‌کند. امتیاز دور فعال نباید کاهش یابد. دورهای متعلق به بازیکن دیگر با `404` رد می‌شوند.
- اگر بازنگری ارسالی قدیمی باشد، پاسخ `409` با `{"error":"revision_conflict","current":{...}}` برمی‌گردد. کلاینت باید دادهٔ فعلی را بررسی کند و فقط پس از حل تداخل دوباره ذخیره کند. از تکرار کورکورانهٔ درخواست با شمارهٔ جدید خودداری کنید.

نام در حساب تنها وقتی ثبت می‌شود که رکورد نام‌گذاری شده باشد. ساخت حساب نیازی به نام ندارد. فیلدهای سکه، امتیاز، XP و آیتم‌های خریداری‌شده ورودی کلاینت هستند؛ سرور شکل و محدودهٔ داده را بررسی می‌کند، نه درستی پاداش‌های داخل بازی را.

### اتصال بازی فعلی در مرحلهٔ بعد

1. هنگام شروع برنامه، پس از ورود، `GET /v1/me/progress` را بخوانید. اگر `progress` سرور `null` است و مرورگر ذخیرهٔ محلی دارد، `roadster-progress` و مقدار عددی `roadster-xp` را با `revision:0` ارسال کنید. اگر سرور ذخیره دارد، همان را مبنای شروع قرار دهید.
2. هنگام شروع/شروع دوبارهٔ بازی، `POST /v1/runs` را فراخوانی کنید؛ سپس `player.resetRun()` و ذخیرهٔ پیشرفت با بازنگری برگشتی را انجام دهید.
3. بعد از خرید، توقف، خروج از صفحه، ثبت نام رکورد و پایان دور، ذخیره را با `PUT /v1/me/progress` بفرستید. برای هر فریم یا هر ثانیه درخواست شبکه نفرستید؛ checkpoint دوره‌ای با فاصلهٔ بیشتر و ذخیرهٔ فوری در رویدادهای مهم مناسب‌تر است.
4. در خطای `409`، نسخهٔ سرور را با وضعیت محلی مقایسه کنید. ادغام خودکار موجودی و خرید بدون منطق اقتصادِ سمت سرور ممکن است داده را ناسازگار کند؛ در نسخهٔ اول نسخهٔ سرور را نشان دهید و از بازیکن بخواهید یکی از نسخه‌ها را انتخاب کند.

## مسابقهٔ برخط

هر مرورگر به `GET /v1/game/socket` متصل می‌شود و پس از ارسال پیام `identify` در فهرست بازیکنان آنلاین قرار می‌گیرد. میزبان می‌تواند به هر تعداد بازیکن آزاد دعوت بفرستد. دعوت ۴۵ ثانیه اعتبار دارد و مهمان با `accept` یا `decline` پاسخ می‌دهد. میزبان مسابقه را با دست‌کم دو بازیکن شروع می‌کند. سرور شروع و پایان مسابقه را تعیین می‌کند و وضعیت رانندگی را برای اعضای اتاق پخش می‌کند. هیچ سقف ثابتی برای تعداد اعضای اتاق تعریف نشده است؛ ظرفیت واقعی به منابع سرویس و پهنای باند بستگی دارد.

فهرست بازیکنان و اتاق‌ها در حافظهٔ همان فرایند هستند. سرویس باید یک instance داشته باشد؛ بازنشر یا restart اتاق‌های فعال را از بین می‌برد و کلاینت‌ها دوباره متصل می‌شوند. برای چند instance یا نگهداری مسابقه در زمان restart باید state و پیام‌رسانی بین instanceها اضافه شود.

## استقرار در Render

1. مخزن API را به Render متصل کنید و Blueprint فایل [`render.yaml`](render.yaml) را بسازید. این تنظیم یک Go Web Service با یک instance و health check مسیر `/health` تعریف می‌کند.
2. مقدار محرمانهٔ `DATABASE_URL` را در Render وارد کنید. می‌توان از همان PostgreSQL فعلی با connection string دارای pooling و TLS استفاده کرد. روی پایگاه داده، migrationهای `001_init.sql` و `002_promos_telegram.sql` را یک بار اجرا کنید.
3. `GAME_ORIGIN` و `RP_ID` در Blueprint برای `https://roadster-game-ten.vercel.app` تنظیم شده‌اند. اگر دامنهٔ بازی تغییر کند، هر دو را مطابق دامنهٔ جدید تغییر دهید. برای Telegram نیز متغیرهای اختیاری بات را در Render تنظیم کنید.
4. پس از آماده شدن سرویس، `/health` را بررسی کنید و `VITE_ROADSTER_API_URL` را روی آدرس `https://...onrender.com` در پروژهٔ فرانت Vercel بگذارید و آن را دوباره منتشر کنید. این آدرس برای Account & Sync و WebSocket مسابقه استفاده می‌شود.

## آزمون

```sh
go test ./...
TEST_DATABASE_URL='postgres://user:password@localhost:5432/roadster_test?sslmode=disable' go test ./...
```

آزمون‌های پایگاه داده فقط با `TEST_DATABASE_URL` اجرا می‌شوند و برای هر اجرا schema موقت می‌سازند. این URL را به یک پایگاه دادهٔ آزمایشی اختصاص دهید. آزمون مرورگری Passkey با صفحهٔ `examples/passkey.html` دستی انجام می‌شود.
