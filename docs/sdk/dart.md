# Dart and Flutter SDK

The `xenibase` package works in Flutter apps (iOS, Android, web, desktop)
and in plain Dart.

    dart pub add xenibase      # or: flutter pub add xenibase

```dart
import 'package:xenibase/xenibase.dart';

final xb = XenibaseClient('https://k7f3m2q9.api.xenibase.ng', 'xb_pub_…', sessionStorage: SecureSessionStorage());
```

Use the publishable key in apps; the secret key skips row-level security
and belongs on servers only.

## Errors

API failures throw `XenibaseException` with `status`, `code`, `message` and
`requestId`. A network failure has `status` 0 and `code`
`network_error`.

```dart
try {
  await xb.auth.verifyOtp(type: 'whatsapp', phone: phone, token: code);
} on XenibaseException catch (e) {
  if (e.code == 'otp_expired') …
}
```

## Data

```dart
final page = await xb.data.from('todos')
    .select('id, title, done')
    .eq('done', false)
    .or([const Cond('owner_id', Op.eq, 'me'), const Cond('shared', Op.is_, true)])
    .order('created_at', descending: true)
    .limit(20)
    .get(Todo.fromJson); // a generated class, or omit for maps
final more = await xb.data.from('todos').select().cursor(page.nextCursor).get(Todo.fromJson);

final w = await xb.data.from('todos').insert({'title': 'Buy milk'}).run(); // w.affected, w.rows
await xb.data.from('todos').update({'done': true}).eq('id', 42).run();
await xb.data.from('todos').delete().byKey(42).run();
await xb.data.rpc('close_cart', {'cart_id': 4});
```

`xenibase gen types --lang dart` writes a class per table and view, each
with `fromJson` and `toJson`.

### Live queries

`live` keeps a query's result current. It reads now, then reads again when
the table changes, after a resync and after a reconnect. Turn realtime on
for the table first.

```dart
final stop = xb.data.from('todos').select().eq('done', false)
    .live<Todo>((page, error) => setState(() => todos = page?.rows ?? todos), fromJson: Todo.fromJson);
// in dispose(): stop();
```

## Auth

```dart
await xb.auth.signInWithOtp(phone: '08031234567', channel: 'whatsapp');
final session = await xb.auth.verifyOtp(type: 'whatsapp', phone: '+2348031234567', token: '123456');
await xb.auth.signInWithPassword(email: email, password: password);
await xb.auth.signUp(email: email, password: password, data: {'name': 'Ada'});
await xb.auth.signOut();
xb.auth.onAuthStateChange.listen((e) => …); // (AuthEvent, Session?)
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

1. `xb.auth.oauthUrl(provider, redirectTo)` returns the authorize URL and
   a verifier.
2. Open the URL with `flutter_web_auth_2`.
3. Pass the `code` from the redirect, with the verifier, to
   `xb.auth.exchangeCode(code, verifier)`.

The [Flutter guide](../guides/flutter.md) has the whole flow.

## Storage

```dart
final avatars = xb.storage.bucket('avatars');
await avatars.upload('${session.userId}/me.jpg', bytes, contentType: 'image/jpeg', upsert: true);
final bytes = await avatars.download('${session.userId}/me.jpg');
final url = await avatars.signedUrl('${session.userId}/me.jpg', expiresIn: const Duration(hours: 1));
```

## Realtime

```dart
final ch = xb.realtime.channel('orders')
    .onChange(table: 'orders', event: 'INSERT', callback: (c) => add(c.record))
    .onBroadcast('ping', (event, payload) => …)
    .onResync(refetch)
    .subscribe((status, error) => …);
await ch.unsubscribe();
```

The socket reconnects by itself. Afterwards it rejoins each channel and
calls `onResync`, because changes made while it was away were missed.
