# Flutter

This guide covers a Flutter app that:

- signs users in with a WhatsApp code or Google;
- keeps the session in secure storage;
- shows each user's rows live;
- uploads files.

The [auth sample](../examples/flutter-auth/README.md) is a runnable
version of the sign-in part.

## Install

    flutter pub add pgdock flutter_secure_storage flutter_web_auth_2
    pgdock gen types --lang dart --project my-app -o lib/database_types.dart

```dart
// lib/pgdock.dart
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:pgdock/pgdock.dart';

class SecureSessionStorage implements SessionStorage {
  final _s = const FlutterSecureStorage();
  @override
  Future<String?> read(String key) => _s.read(key: key);
  @override
  Future<void> write(String key, String value) => _s.write(key: key, value: value);
  @override
  Future<void> delete(String key) => _s.delete(key: key);
}

final pgd = PgdockClient('https://k7f3m2q9.api.pgdock.ng', 'pgd_pub_…', sessionStorage: SecureSessionStorage());
```

## Sign in with a WhatsApp code

```dart
Future<void> sendCode(String phone) => pgd.auth.signInWithOtp(phone: phone, channel: 'whatsapp');

Future<String?> verify(String phone, String code) async {
  try {
    await pgd.auth.verifyOtp(type: 'whatsapp', phone: phone, token: code);
    return null;
  } on PgdockException catch (e) {
    return e.code == 'otp_expired' ? 'That code has expired' : e.message;
  }
}
```

## Route on the session

```dart
class Root extends StatelessWidget {
  const Root({super.key});
  @override
  Widget build(BuildContext context) => StreamBuilder(
        stream: pgd.auth.onAuthStateChange,
        builder: (context, snap) {
          if (!snap.hasData) return const Center(child: CircularProgressIndicator());
          final (_, session) = snap.data!;
          return session == null ? const SignInPage() : const NotesPage();
        },
      );
}
```

The first event is the stored session (`AuthEvent.initialSession`).
Tokens refresh by themselves a minute before they expire.

## A live list

```dart
class NotesPage extends StatefulWidget {
  const NotesPage({super.key});
  @override
  State<NotesPage> createState() => _NotesPageState();
}

class _NotesPageState extends State<NotesPage> {
  List<Note> notes = [];
  late final void Function() stop;

  @override
  void initState() {
    super.initState();
    stop = pgd.data.from('notes').select().order('created_at', descending: true).live<Note>(
          (page, error) => setState(() => notes = page?.rows ?? notes),
          fromJson: Note.fromJson,
        );
  }

  @override
  void dispose() {
    stop();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListView(children: [for (final n in notes) ListTile(title: Text(n.body))]);
}
```

Turn realtime on for the table (`SELECT pgd_realtime.enable('notes')`).
After a dropped connection, the client reconnects and refetches the list.

## Google

```dart
import 'package:flutter_web_auth_2/flutter_web_auth_2.dart';

Future<void> signInWithGoogle() async {
  const redirect = 'com.example.app://auth-callback'; // allowed under Authentication → URL configuration
  final (:url, :verifier) = pgd.auth.oauthUrl('google', redirect);
  final result = await FlutterWebAuth2.authenticate(url: url.toString(), callbackUrlScheme: 'com.example.app');
  final code = Uri.parse(result).queryParameters['code'];
  if (code != null) await pgd.auth.exchangeCode(code, verifier);
}
```

## Files

```dart
final uid = pgd.auth.currentSession!.userId;
await pgd.storage.bucket('avatars').upload('$uid/me.jpg', await file.readAsBytes(), contentType: 'image/jpeg', upsert: true);
final url = await pgd.storage.bucket('avatars').signedUrl('$uid/me.jpg');
Image.network(url.toString());
```
