# راهنمای اتصال بازی Roadster به API

نشانی Production: `https://roadster-api.vercel.app`. پیش از اتصال واقعی، پاسخ `GET /health` باید `200` و `{"status":"ok"}` باشد. این فایل قرارداد نسخهٔ `v1` را توضیح می‌دهد؛ مسیرهای بازی در این مخزن تغییر نکرده‌اند.

## تنظیمات مرورگر و ورود

- تمام درخواست‌های JSON با `Content-Type: application/json` فرستاده می‌شوند. برای مسیرهای خصوصی، `Authorization: Bearer <token>` لازم است.
- CORS دامنهٔ دقیق `GAME_ORIGIN` و برای توسعه `http://localhost:5173` را می‌پذیرد. مقدار Production بازی فعلی `https://roadster-game-ten.vercel.app` است؛ `RP_ID` باید `roadster-game-ten.vercel.app` باشد. دامنهٔ سفارشی به تنظیم دوبارهٔ هر دو نیاز دارد.
- Passkey باید از صفحه‌ای در origin بازی ساخته/استفاده شود. API منتشرشده برای `http://localhost:5173` از RP ID جداگانهٔ `localhost` استفاده می‌کند. Passkey و حسابی که آنجا می‌سازید با Passkey دامنهٔ Production قابل ورود نیست؛ برای بررسی حساب واقعی، فرانت را روی دامنهٔ Production آزمایش کنید.
- `deviceId` یک UUID تصادفی نصب مرورگر است، نه شناسهٔ سخت‌افزار. آن را یک بار با `crypto.randomUUID()` تولید و در `localStorage` نگه دارید. اگر ذخیرهٔ مرورگر پاک شد، UUID جدید بسازید و با Passkey وارد همان حساب شوید.
- توکن نشست ۳۰ روز اعتبار دارد. آن را مانند رمز عبور محافظت کنید؛ از قرار دادن در URL، log یا پیام خطا خودداری کنید. `POST /v1/auth/logout` آن را باطل می‌کند.

### درخواست‌ها

| روش و مسیر | بدنه | پاسخ موفق |
| --- | --- | --- |
| `POST /v1/auth/register/options` | `{"deviceId":"<uuid>"}` | `{"ceremonyId":"<uuid>","options":{"publicKey":...}}` |
| `POST /v1/auth/register/verify` | `{"ceremonyId":"<uuid>","credential":<credential.toJSON()>}` | `201 {"playerId":"<uuid>","token":"...","expiresIn":2592000}` |
| `POST /v1/auth/login/options` | `deviceId` همین نصب مرورگر؛ در دستگاه دوم UUID تازه | مانند گزینه‌های ثبت‌نام |
| `POST /v1/auth/login/verify` | همان شکل تأیید | `200` و شناسه، توکن، انقضا |
| `POST /v1/auth/logout` | بدون بدنه، با Bearer | `204` |

چالش ورود/ثبت‌نام ۵ دقیقه‌ای و یک‌بارمصرف است. اگر کاربر پنجرهٔ Passkey را لغو کند یا چالش منقضی شود، مرحلهٔ options را دوباره اجرا کنید. ثبت‌نام نیازی به نام بازیکن ندارد. Passkey همگام‌شونده می‌تواند دستگاه دوم را به همان حساب وارد کند؛ بازیابی مستقل بدون Passkey در نسخهٔ اول وجود ندارد.

```ts
const API = 'https://roadster-api.vercel.app';
const deviceKey = 'roadster-device-id';
let deviceId = localStorage.getItem(deviceKey);
if (!deviceId) {
  deviceId = crypto.randomUUID();
  localStorage.setItem(deviceKey, deviceId);
}

async function api<T>(path: string, method = 'GET', body?: unknown, token?: string): Promise<T> {
  const response = await fetch(API + path, {
    method,
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (response.status === 204) return undefined as T;
  const data = await response.json();
  if (!response.ok) throw Object.assign(new Error(data.error), { status: response.status, data });
  return data as T;
}

type Auth = { playerId: string; token: string; expiresIn: number };
async function usePasskey(mode: 'register' | 'login'): Promise<Auth> {
  const begin = await api<{ ceremonyId: string; options: { publicKey: any } }>(
    `/v1/auth/${mode}/options`, 'POST', { deviceId });
  const publicKey = mode === 'register'
    ? PublicKeyCredential.parseCreationOptionsFromJSON(begin.options.publicKey)
    : PublicKeyCredential.parseRequestOptionsFromJSON(begin.options.publicKey);
  const credential = mode === 'register'
    ? await navigator.credentials.create({ publicKey: publicKey as PublicKeyCredentialCreationOptions })
    : await navigator.credentials.get({ publicKey: publicKey as PublicKeyCredentialRequestOptions });
  if (!credential) throw new Error('passkey_cancelled');
  return api<Auth>(`/v1/auth/${mode}/verify`, 'POST', {
    ceremonyId: begin.ceremonyId,
    credential: (credential as PublicKeyCredential).toJSON(),
  });
}
```

بسته به نسخهٔ TypeScript/DOM پروژه، برای `parseCreationOptionsFromJSON` و `parseRequestOptionsFromJSON` ممکن است به DOM lib جدید نیاز باشد. نمونهٔ قابل‌اجرای ساده در [`examples/passkey.html`](../examples/passkey.html) قرار دارد.

## پیشرفت، دور بازی و همگام‌سازی

| روش و مسیر | کاربرد | پاسخ موفق |
| --- | --- | --- |
| `GET /v1/me/progress` | خواندن ذخیره | `{"revision":0,"progress":null,"xp":0}` برای حساب تازه؛ وگرنه آخرین داده |
| `PUT /v1/me/progress` | ذخیره با بازنگری | `{"revision":<جدید>}` |
| `POST /v1/runs` | ساخت دور جدید | `201 {"runId":"<uuid>","status":"active","revision":<جدید>}`؛ تکرار همان کلید: `200` |

`progress` باید همان شیء JSON کلید `roadster-progress` بازی با `version:1` باشد؛ XP عددی جداگانه از کلید `roadster-xp` است. نمونهٔ کامل:

```json
{
  "revision": 0,
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
  "xp": 200
}
```

`runId` و `runStatus` اختیاری‌اند. هنگام ذخیرهٔ دور فعال، `runId` برگشتی از `/v1/runs` و `runStatus:"active"` را اضافه کنید؛ در پایان دور `"finished"` بفرستید. ذخیرهٔ خرید یا تغییر بیرون از دور می‌تواند این دو فیلد را حذف کند. `score` یک دور فعال نباید نسبت به ذخیرهٔ قبلی آن دور کاهش یابد. ساخت دور جدید `score` را صفر می‌کند و سکه، رکورد، خریدها و XP را حفظ می‌کند. دور فعال قبلی `abandoned` می‌شود.

برای هر بار فشردن «شروع بازی» یک `idempotencyKey = crypto.randomUUID()` تولید کنید؛ در retry همان کلید را دوباره بفرستید. پس از پاسخ، `revision` محلی را به مقدار برگشتی تغییر دهید و سپس وضعیت `score:0` را در حافظه/ذخیرهٔ محلی بازی اعمال کنید. از ساخت کلید جدید برای retry استفاده نکنید.

در اولین ورود، ابتدا `GET /v1/me/progress` را بخوانید. اگر `progress:null` بود، ذخیرهٔ محلی موجود را با `revision:0` به سرور بفرستید. اگر پیشرفت سرور موجود بود، آن را مبنای بازی قرار دهید. اگر هنوز ذخیرهٔ محلی ندارید، بازی را اجرا کنید تا شیء ذخیرهٔ نسخهٔ ۱ ایجاد شود؛ سپس آن را با `PUT` بفرستید. ثبت کد سکه نیز به این ذخیرهٔ اولیه نیاز دارد.

هر `PUT` باید `revision` آخرین پاسخ موفق را بفرستد. خطای `409 revision_conflict` این شکل را دارد:

```json
{"error":"revision_conflict","current":{"revision":4,"progress":{},"xp":200}}
```

پس از `409` دادهٔ `current` را با وضعیت محلی مقایسه کنید و فقط با تصمیم آگاهانهٔ بازیکن/منطق ادغام، دوباره ذخیره کنید. تکرار کورکورانهٔ درخواست با `revision` جدید می‌تواند سکهٔ حاصل از کد یا پیشرفت دستگاه دیگر را پاک کند. ذخیرهٔ فوری بعد از خرید، دریافت کد، ثبت رکورد و پایان دور مناسب است؛ در جریان بازی checkpoint کم‌تعداد بفرستید. از ارسال شبکه در هر فریم پرهیز کنید.

## کد سکه

`POST /v1/me/redeem` با Bearer و `{"code":"RDST-..."}` → `200 {"coinsAdded":50,"coins":225,"revision":5}`. سرور سکه را به ذخیرهٔ JSON اضافه و `revision` را یک واحد زیاد می‌کند. پس از موفقیت، نسخهٔ تازه را با `GET /v1/me/progress` بخوانید و همان را به بازی اعمال کنید؛ از افزودن دوبارهٔ `coinsAdded` در کلاینت خودداری کنید. هر بازیکن یک کد مشخص را فقط یک بار مصرف می‌کند؛ هر کد سقف کل مصرف و تاریخ انقضا دارد. حروف کوچک کد پذیرفته می‌شوند.

خطاهای مهم: `400 invalid_code`، `410 code_expired`، `409 code_exhausted`، `409 code_already_redeemed`، `409 progress_required` (هنوز ذخیرهٔ اولیه وجود ندارد) و `409 balance_overflow`.

## اتصال تلگرام

1. کاربر با Passkey وارد بازی شود و ذخیرهٔ اولیه را همگام کند.
2. `GET /v1/me/telegram` → `{"linked":false,"botUsername":"RoadsterBot"}`. نام بات مقدار تنظیم‌شدهٔ محیط است.
3. `POST /v1/me/telegram/link` با Bearer و بدون بدنه → `{"url":"https://t.me/<bot>?start=<code>","expiresIn":600}`. URL را به کاربر نشان دهید یا با کلیک او باز کنید؛ کد اتصال را در log ننویسید. درخواست لینک جدید، لینک قبلی را باطل می‌کند.
4. کاربر در گفت‌وگوی خصوصی بات روی Start بزند. `GET /v1/me/telegram` پس از دریافت پیام بات باید `linked:true` برگرداند.
5. `DELETE /v1/me/telegram` با Bearer → `204` و اتصال را قطع می‌کند.

بات دستورهای `/balance`، `/record` و `/redeem CODE` را در گفت‌وگوی خصوصی می‌پذیرد. `/redeem` همان تراکنش و محدودیت مسیر HTTP را به‌کار می‌برد. پیام تکراری webhook سکهٔ اضافه ایجاد نمی‌کند. پس از ثبت کد از طریق بات، بازی باید `GET /v1/me/progress` را دوباره بخواند؛ ذخیرهٔ محلی قدیمی با خطای `revision_conflict` روبه‌رو می‌شود.

## خطاها و محدودیت‌ها

پاسخ خطا `{"error":"code"}` است. `401 unauthorized` یعنی توکن غایب، نامعتبر یا منقضی است؛ دوباره با Passkey وارد شوید. `403 origin_not_allowed` یعنی دامنهٔ بازی با `GAME_ORIGIN` یکسان نیست. `400 ceremony_expired_or_used` یعنی گزینه‌های Passkey را دوباره درخواست کنید. `404 run_not_found` یعنی دور متعلق به این بازیکن نیست. `409 run_not_active_or_score_reduced` یعنی دور بسته شده یا امتیاز کاهش یافته است. `503 database_unavailable`/`telegram_unavailable` خطای آماده‌سازی سرویس است.

در این نسخه، سکه و XP بازی به‌جز اعتبار کد توسط فرانت محاسبه می‌شوند و `PUT` می‌تواند مقدار جدید را ارسال کند. بنابراین این API هنوز مبنای امن برای اقتصاد رقابتی یا جدول رتبه‌بندی نیست.
