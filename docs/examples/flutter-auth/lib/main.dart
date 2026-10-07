import 'package:flutter/material.dart';

import 'pgdock_auth.dart';

// Fill these in from Project → Settings → API.
const projectURL = String.fromEnvironment('PGDOCK_URL', defaultValue: 'https://<ref>.<domain>');
const publishableKey = String.fromEnvironment('PGDOCK_KEY', defaultValue: '<publishable key>');
const redirect = 'com.example.pgdocksample://login-callback';

void main() => runApp(const SampleApp());

class SampleApp extends StatelessWidget {
  const SampleApp({super.key});
  @override
  Widget build(BuildContext context) => MaterialApp(
        title: 'PGDock sign-in',
        theme: ThemeData(colorSchemeSeed: const Color(0xFF3ECF8E), useMaterial3: true),
        home: const SignInPage(),
      );
}

class SignInPage extends StatefulWidget {
  const SignInPage({super.key});
  @override
  State<SignInPage> createState() => _SignInPageState();
}

class _SignInPageState extends State<SignInPage> {
  final client = PgDock(projectURL, publishableKey);
  final phone = TextEditingController();
  final code = TextEditingController();
  bool codeSent = false;
  bool busy = false;
  String? error;
  List<Map<String, dynamic>> docs = [];

  Future<void> run(Future<void> Function() f) async {
    setState(() {
      busy = true;
      error = null;
    });
    try {
      await f();
    } on AuthError catch (e) {
      error = e.message;
    } catch (e) {
      error = '$e';
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  Future<void> loadDocs() async {
    // Row-level security returns only the documents of the org the
    // custom-claims hook put in the token.
    docs = await client.select('docs', query: 'select=title&order=title');
  }

  @override
  Widget build(BuildContext context) {
    final s = client.session;
    return Scaffold(
      appBar: AppBar(title: const Text('PGDock sign-in')),
      body: Padding(
        padding: const EdgeInsets.all(24),
        child: s != null ? signedIn(s) : signIn(),
      ),
    );
  }

  Widget signIn() => Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        TextField(
          controller: phone,
          keyboardType: TextInputType.phone,
          decoration: const InputDecoration(labelText: 'Phone number', hintText: '0803 123 4567'),
        ),
        const SizedBox(height: 12),
        if (codeSent)
          TextField(
            controller: code,
            keyboardType: TextInputType.number,
            maxLength: 6,
            decoration: const InputDecoration(labelText: 'Code from WhatsApp'),
          ),
        FilledButton(
          onPressed: busy
              ? null
              : () => run(() async {
                    if (!codeSent) {
                      await client.sendCode(phone.text);
                      codeSent = true;
                    } else {
                      await client.verifyCode(phone.text, code.text);
                      await loadDocs();
                    }
                  }),
          child: Text(codeSent ? 'Sign in' : 'Send a code on WhatsApp'),
        ),
        const Divider(height: 40),
        OutlinedButton.icon(
          icon: const Icon(Icons.login),
          label: const Text('Continue with Google'),
          onPressed: busy
              ? null
              : () => run(() async {
                    await client.signInWith('google', redirect);
                    await loadDocs();
                  }),
        ),
        if (error != null) Padding(padding: const EdgeInsets.only(top: 16), child: Text(error!, style: const TextStyle(color: Colors.red))),
      ]);

  Widget signedIn(Session s) => Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Text('Signed in as ${s.user['phone'] ?? s.user['email']}', style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 16),
        Text("Your org's documents", style: Theme.of(context).textTheme.labelLarge),
        for (final d in docs) ListTile(title: Text('${d['title']}')),
        if (docs.isEmpty) const Text('None yet (or no org: add a members row, then refresh).'),
        const SizedBox(height: 16),
        Row(children: [
          TextButton(onPressed: busy ? null : () => run(() async { await client.refresh(); await loadDocs(); }), child: const Text('Refresh')),
          TextButton(onPressed: busy ? null : () => run(client.signOut), child: const Text('Sign out')),
        ]),
        if (error != null) Text(error!, style: const TextStyle(color: Colors.red)),
      ]);
}
