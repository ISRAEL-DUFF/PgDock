// Runs against a real edge when TestSDKs (test/integration) sets PGDOCK_SDK_URL.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:pgdock/pgdock.dart';
import 'package:test/test.dart';

void main() {
  final base = Platform.environment['PGDOCK_SDK_URL'];
  test('live', () async {
    final pgd = PgdockClient(base!, Platform.environment['PGDOCK_SDK_PUB']!);
    final s = await pgd.auth.signInWithPassword(email: 'dart@sdk.test', password: Platform.environment['PGDOCK_SDK_PASSWORD']!);
    final uid = s!.userId;
    expect((await pgd.data.health())['role'], 'user');

    // Realtime.
    final changes = <Change>[];
    final subscribed = Completer<void>();
    final ch = pgd.realtime.channel('notes-dart').onChange(table: 'notes', event: 'INSERT', callback: changes.add).subscribe((st, err) {
      if (st == 'SUBSCRIBED' && !subscribed.isCompleted) subscribed.complete();
    });
    await subscribed.future.timeout(const Duration(seconds: 15));

    // A live query.
    final pages = <Page<Map<String, dynamic>>>[];
    final stop = pgd.data.from('notes').select('body').order('id').live<Map<String, dynamic>>((p, e) {
      if (p != null) pages.add(p);
    });

    final w = await pgd.data.from('notes').insert({'body': 'dart 1'}).run();
    expect(w.affected, 1);
    expect(w.rows.first['owner_id'], uid);
    await _until(() => changes.any((c) => c.record['body'] == 'dart 1'));
    await _until(() => pages.isNotEmpty && pages.last.rows.length == 1);

    final page = await pgd.data.from('notes').select('id,body').count().get();
    expect(page.rows.map((r) => r['body']), ['dart 1']);
    expect(page.count, 1);

    // Refresh.
    final before = s.accessToken;
    final s2 = await pgd.auth.refreshSession();
    expect(s2.accessToken, isNot(before));
    expect((await pgd.data.from('notes').select().get()).rows.length, 1);

    // Files.
    final b = pgd.storage.bucket('files');
    await b.upload('$uid/hello.txt', utf8.encode('hello from dart'), contentType: 'text/plain');
    expect(utf8.decode(await b.download('$uid/hello.txt')), 'hello from dart');
    expect((await b.list(prefix: '$uid/')).items.map((i) => i['name']), ['hello.txt']);
    final signed = await b.signedUrl('$uid/hello.txt');
    final bu = Uri.parse(base);
    final res = await HttpClient().getUrl(signed.replace(scheme: bu.scheme, host: bu.host, port: bu.port)).then((r) => r.close());
    expect(await res.transform(utf8.decoder).join(), 'hello from dart');
    await expectLater(b.upload('someone-else/x.txt', utf8.encode('no'), contentType: 'text/plain'),
        throwsA(isA<PgdockException>().having((e) => e.status, 'status', 403)));

    // Errors.
    await expectLater(pgd.data.from('nope').select().get(),
        throwsA(isA<PgdockException>().having((e) => e.code, 'code', 'unknown_table').having((e) => e.status, 'status', 404)));

    stop();
    await ch.unsubscribe();
    await pgd.auth.signOut();
    expect(await pgd.auth.getSession(), isNull);
    pgd.dispose();
  }, skip: base == null ? 'PGDOCK_SDK_URL not set' : false, timeout: const Timeout(Duration(minutes: 2)));
}

Future<void> _until(bool Function() ok, [Duration d = const Duration(seconds: 10)]) async {
  final end = DateTime.now().add(d);
  while (!ok()) {
    if (DateTime.now().isAfter(end)) throw TimeoutException('condition not met');
    await Future<void>.delayed(const Duration(milliseconds: 100));
  }
}
