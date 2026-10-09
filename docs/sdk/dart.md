# Dart and Flutter SDK

The `pgdock` package works in Flutter apps (iOS, Android, web, desktop)
and in plain Dart.

    dart pub add pgdock      # or: flutter pub add pgdock

```dart
import 'package:pgdock/pgdock.dart';

final pgd = PgdockClient('https://k7f3m2q9.api.pgdock.ng', 'pgd_pub_…', sessionStorage: SecureSessionStorage());
```

Use the publishable key in apps; the secret key skips row-level security
and belongs on servers only.

## Errors

API failures throw `PgdockException` with `status`, `code`, `message` and
`requestId`. A network failure has `status` 0 and `code`
`network_error`.

```dart
try {
  await pgd.auth.verifyOtp(type: 'whatsapp', phone: phone, token: code);
} on PgdockException catch (e) {
  if (e.code == 'otp_expired') …
}
```

## Data

```dart
final page = await pgd.data.from('todos')
    .select('id, title, done')
    .eq('done', false)
    .or([const Cond('owner_id', Op.eq, 'me'), const Cond('shared', Op.is_, true)])
    .order('created_at', descending: true)
    .limit(20)
    .get(Todo.fromJson); // a generated class, or omit for maps
final more = await pgd.data.from('todos').select().cursor(page.nextCursor).get(Todo.fromJson);

final w = await pgd.data.from('todos').insert({'title': 'Buy milk'}).run(); // w.affected, w.rows
await pgd.data.from('todos').update({'done': true}).eq('id', 42).run();
await pgd.data.from('todos').delete().byKey(42).run();
await pgd.data.rpc('close_cart', {'cart_id': 4});
```

`pgdock gen types --lang dart` writes a class per table and view, each
with `fromJson` and `toJson`.

### Live queries

`live` keeps a query's result current. It reads now, then reads again when
the table changes, after a resync and after a reconnect. Turn realtime on
for the table first.

```dart
final stop = pgd.data.from('todos').select().eq('done', false)
    .live<Todo>((page, error) => setState(() => todos = page?.rows ?? todos), fromJson: Todo.fromJson);
// in dispose(): stop();
```

## Auth

```dart
await pgd.auth.signInWithOtp(phone: '08031234567', channel: 'whatsapp');
final session = await pgd.auth.verifyOtp(type: 'whatsapp', phone: '+2348031234567', token: '123456');
await pgd.auth.signInWithPassword(email: email, password: password);
await pgd.auth.signUp(email: email, password: password, data: {'name': 'Ada'});
await pgd.auth.signOut();
pgd.auth.onAuthStateChange.listen((e) => …); // (AuthEvent, Session?)
```

- **Refresh.** Tokens refresh a minute before they expire, and before any
  request that would carry an expired one. Only one refresh runs at a
  time.
- **Sessions.** They live in memory unless you pass a `SessionStorage`.
  In Flutter, wrap `flutter_secure_storage`:

```dart
class SecureSessionStorage implements SessionStorage {
  final _s = const FlutterSecureStorage();
  @override Future<String?> read(String key) => _s.read(key: key);
  @override Future<void> write(String key, String value) => _s.write(key: key, value: value);
  @override Future<void> delete(String key) => _s.delete(key: key);
}
```

**OAuth** (Google, Apple, …) uses PKCE:

1. `pgd.auth.oauthUrl(provider, redirectTo)` returns the authorize URL and
   a verifier.
2. Open the URL with `flutter_web_auth_2`.
3. Pass the `code` from the redirect, with the verifier, to
   `pgd.auth.exchangeCode(code, verifier)`.

The [Flutter guide](../guides/flutter.md) has the whole flow.

## Storage

```dart
final avatars = pgd.storage.bucket('avatars');
await avatars.upload('${session.userId}/me.jpg', bytes, contentType: 'image/jpeg', upsert: true);
final bytes = await avatars.download('${session.userId}/me.jpg');
final url = await avatars.signedUrl('${session.userId}/me.jpg', expiresIn: const Duration(hours: 1));
```

## Realtime

```dart
final ch = pgd.realtime.channel('orders')
    .onChange(table: 'orders', event: 'INSERT', callback: (c) => add(c.record))
    .onBroadcast('ping', (event, payload) => …)
    .onResync(refetch)
    .subscribe((status, error) => …);
await ch.unsubscribe();
```

The socket reconnects by itself. Afterwards it rejoins each channel and
calls `onResync`, because changes made while it was away were missed.
