# xenibase

Xenibase's client for Flutter and Dart. It covers the data API, auth
(phone and WhatsApp codes, email, OAuth), storage, realtime and live
queries.

    flutter pub add xenibase

```dart
final xb = XenibaseClient('https://<ref>.api.xenibase.ng', 'xb_pub_…');
await xb.auth.signInWithOtp(phone: '+2348012345678', channel: 'whatsapp');
final page = await xb.data.from('todos').select('id, title').eq('done', false).get();
```

The full reference is
[docs/sdk/dart.md](https://github.com/ISRAEL-DUFF/Xenibase/blob/main/docs/sdk/dart.md),
and the [Flutter guide](https://github.com/ISRAEL-DUFF/Xenibase/blob/main/docs/guides/flutter.md)
walks through a whole app.
