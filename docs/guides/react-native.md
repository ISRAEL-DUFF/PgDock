# React Native

`@xenibase/client` runs in React Native (Expo or bare). This guide signs a
user in with a WhatsApp code, keeps them signed in across launches,
reads their rows, keeps the list live, and uploads a photo.

## Install

    npx expo install @xenibase/client @react-native-async-storage/async-storage react-native-get-random-values expo-web-browser

`react-native-get-random-values` provides `crypto.getRandomValues`, which
the OAuth sign-in (PKCE) needs. Import it once, before the client:

```ts
// lib/xenibase.ts
import "react-native-get-random-values";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { createClient } from "@xenibase/client";
import type { Database } from "./database.types"; // xenibase gen types --lang ts

export const xb = createClient<Database>("https://k7f3m2q9.api.xenibase.ng", "xb_pub_…", {
  auth: { storage: AsyncStorage, detectSessionInUrl: false },
});
```

React Native has `fetch` and `WebSocket` built in, so data and realtime
need nothing else.

## Sign in with WhatsApp

First, turn WhatsApp on in Authentication → Phone. Numbers can be written
the local way: `0803…` is read as Nigerian.

```tsx
import { useState } from "react";
import { Button, TextInput, View, Text } from "react-native";
import { xb } from "./lib/xenibase";

export function SignIn() {
  const [phone, setPhone] = useState("");
  const [code, setCode] = useState("");
  const [step, setStep] = useState<"phone" | "code">("phone");
  const [error, setError] = useState<string>();

  const send = async () => {
    const { error } = await xb.auth.signInWithOtp({ phone, channel: "whatsapp" });
    error ? setError(error.message) : setStep("code");
  };
  const verify = async () => {
    const { error } = await xb.auth.verifyOtp({ type: "whatsapp", phone, token: code });
    if (error) setError(error.code === "otp_expired" ? "That code has expired; send another." : error.message);
  };

  return (
    <View>
      {step === "phone" ? (
        <><TextInput value={phone} onChangeText={setPhone} keyboardType="phone-pad" /><Button title="Send code" onPress={send} /></>
      ) : (
        <><TextInput value={code} onChangeText={setCode} keyboardType="number-pad" /><Button title="Sign in" onPress={verify} /></>
      )}
      {error && <Text>{error}</Text>}
    </View>
  );
}
```

## Follow the session

```tsx
export default function App() {
  const [session, setSession] = useState<Session | null | undefined>(undefined);
  useEffect(() => xb.auth.onAuthStateChange((_e, s) => setSession(s)), []);
  if (session === undefined) return null; // loading the stored session
  return session ? <Notes /> : <SignIn />;
}
```

`onAuthStateChange` first reports the stored session (`INITIAL_SESSION`).
After that it reports sign-ins, refreshes and sign-outs. Tokens refresh a
minute before they expire.

The access token lasts an hour by default. When the app comes back to the
foreground, the next request refreshes the token if it needs to.

## A live list

```tsx
function Notes() {
  const [notes, setNotes] = useState<Tables<"notes">[]>([]);
  useEffect(() => xb.data.from("notes").select().order("created_at", "desc").live(({ data }) => data && setNotes(data)), []);
  return <FlatList data={notes} keyExtractor={(n) => String(n.id)} renderItem={({ item }) => <Text>{item.body}</Text>} />;
}
```

Turn realtime on for the table (`SELECT xb_realtime.enable('notes')`).
Mobile connections drop often. The client reconnects, rejoins its
channels and refetches the live query, so the list catches up by itself.

## Upload a photo

```ts
import * as ImagePicker from "expo-image-picker";

const pick = await ImagePicker.launchImageLibraryAsync({ mediaTypes: ["images"], quality: 0.8 });
if (!pick.canceled) {
  const asset = pick.assets[0];
  const blob = await (await fetch(asset.uri)).blob();
  const { data: user } = await xb.auth.getUser();
  await xb.storage.bucket("avatars").upload(`${user!.id}/me.jpg`, blob, { contentType: "image/jpeg", upsert: true });
}
```

The bucket's policy decides who may upload where. The
[storage docs](../backend-services.md#storage) have a per-user folder
policy.

## Google sign-in

```ts
import * as WebBrowser from "expo-web-browser";
import * as Linking from "expo-linking";

const redirectTo = Linking.createURL("auth/callback"); // add it under Authentication → URL configuration
const { data } = await xb.auth.signInWithOAuth({ provider: "google", redirectTo, skipRedirect: true });
const result = await WebBrowser.openAuthSessionAsync(data!.url, redirectTo);
if (result.type === "success") {
  const code = new URL(result.url).searchParams.get("code");
  if (code) await xb.auth.exchangeCodeForSession(code);
}
```

The PKCE verifier is kept in the client's storage between the two calls.
