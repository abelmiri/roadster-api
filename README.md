# Roadster API

یک API مستقل با Go برای ذخیره و همگام‌سازی پیشرفت [Roadster](https://github.com/abelmiri/roadster-game). بازی فعلی همچنان محاسبهٔ سکه، XP و خرید را انجام می‌دهد؛ این سرویس دادهٔ بازیکن را با Passkey به یک حساب متصل می‌کند. برای جدول رتبه‌بندی رقابتی یا اقتصاد مقاوم در برابر تقلب، محاسبهٔ پاداش و خرید باید در آینده به سرور منتقل شود.

## پیش‌نیاز و اجرای محلی

- Go 1.27.3 یا نسخهٔ مشخص‌شده در `go.mod`. نصب رسمی: <https://go.dev/dl/>. روی این پروژه نسخهٔ 1.27.3 آزمایش شده است.
- PostgreSQL و ابزار `psql`. برای Vercel، یک PostgreSQL از Marketplace مانند Neon مناسب است. از connection string دارای pooling و TLS استفاده کنید.
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
GAME_ORIGIN='http://localhost:5173' RP_ID='localhost' go run .
```

متغیرهای لازم در [.env.example](.env.example) فهرست شده‌اند. فایل `.env` به‌صورت خودکار خوانده نمی‌شود؛ متغیرها را در shell یا تنظیمات Vercel قرار دهید. migration را **یک بار** روی هر پایگاه داده اجرا کنید؛ سرور در زمان درخواست جدول نمی‌سازد.

برای آزمایش دستی Passkey، در ترمینال دیگر اجرا کنید:

```sh
python3 -m http.server 5173 --directory examples
```

سپس `http://localhost:5173/passkey.html` را باز کنید، یک Passkey بسازید، دوباره وارد شوید و پیشرفت را بخوانید. Passkey آزمایشی `localhost` برای دامنهٔ Production قابل استفاده نیست.

## قرارداد API

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

## استقرار در Vercel

1. این پوشه را به‌عنوان یک مخزن Git مستقل در GitHub قرار دهید؛ فایل‌های `.env` و `.vercel` را commit نکنید.
2. در داشبورد Vercel از **Add New → Project** مخزن API را انتخاب کنید. Root Directory ریشهٔ همین مخزن و Framework Preset برابر **Go** باشد. `vercel.json` همین preset را تثبیت می‌کند. طبق [مستندات Go Runtime](https://vercel.com/docs/functions/runtimes/go)، `main.go` و `go.mod` ریشه شناسایی می‌شوند و برنامه باید روی `PORT` گوش کند. Go Runtime فعلاً Beta است.
3. از بخش Storage/Marketplace، یک PostgreSQL مانند Neon را متصل کنید. [Vercel Marketplace](https://vercel.com/docs/marketplace-storage) connection string را به متغیرهای پروژه اضافه می‌کند. مقدار pooled connection string را در `DATABASE_URL` قرار دهید و migration را با `psql` روی همان پایگاه داده اجرا کنید. پایگاه داده و Function را در منطقه‌های نزدیک انتخاب کنید.
4. `GAME_ORIGIN` را دقیقاً برابر origin بازی، مثلاً `https://roadster-game.example.com`، و `RP_ID` را برابر نام دامنهٔ بدون `https://` تنظیم کنید. از دامنهٔ ثابت Production استفاده کنید؛ Passkey به دامنه وابسته است. برای Preview، پایگاه داده و origin جدا در نظر بگیرید.
5. Deploy را بزنید، `GET https://<api-domain>/health` را بررسی کنید، سپس جریان ثبت‌نام و ورود را از **origin بازی** آزمایش کنید. هر push بعدی به شاخهٔ Production دوباره منتشر می‌شود؛ جزئیات در [راهنمای اتصال Git به Vercel](https://vercel.com/docs/git) آمده است.

## آزمون

```sh
go test ./...
TEST_DATABASE_URL='postgres://user:password@localhost:5432/roadster_test?sslmode=disable' go test ./...
```

آزمون‌های پایگاه داده فقط با `TEST_DATABASE_URL` اجرا می‌شوند و برای هر اجرا schema موقت می‌سازند. این URL را به یک پایگاه دادهٔ آزمایشی اختصاص دهید. آزمون مرورگری Passkey با صفحهٔ `examples/passkey.html` دستی انجام می‌شود.
