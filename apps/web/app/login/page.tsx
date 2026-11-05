"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { api } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [orgName, setOrgName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (mode === "login") {
        await api.post("/v1/auth/login", { email, password });
      } else {
        await api.post("/v1/auth/register", {
          email,
          password,
          display_name: displayName,
          organization_name: orgName,
        });
      }
      router.replace("/");
    } catch (err) {
      setError(err instanceof Error ? err.message : "something went wrong");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background:
          "radial-gradient(1200px 600px at 50% -10%, rgba(91,141,239,0.14), transparent), var(--bg)",
      }}
    >
      <div className="surface" style={{ width: 400, padding: 32 }}>
        <div style={{ marginBottom: 24 }}>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              marginBottom: 6,
            }}
          >
            <div
              style={{
                width: 34,
                height: 34,
                borderRadius: 9,
                background: "var(--accent)",
                display: "grid",
                placeItems: "center",
                fontWeight: 800,
                fontSize: 17,
                color: "#fff",
              }}
            >
              DA
            </div>
            <span style={{ fontSize: 20, fontWeight: 700 }}>DataAtlas</span>
          </div>
          <p className="text-dim" style={{ fontSize: 13 }}>
            Discover. Understand. Trust. Govern.
          </p>
        </div>

        <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
          {mode === "register" && (
            <>
              <input
                className="input"
                placeholder="Your name"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                required
              />
              <input
                className="input"
                placeholder="Organization name"
                value={orgName}
                onChange={(e) => setOrgName(e.target.value)}
                required
              />
            </>
          )}
          <input
            className="input"
            type="email"
            placeholder="Email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
          <input
            className="input"
            type="password"
            placeholder="Password (12+ characters)"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            minLength={12}
            required
          />
          {error && (
            <div className="badge badge-red" style={{ justifyContent: "center", padding: 8 }}>
              {error}
            </div>
          )}
          <button className="btn btn-primary" disabled={busy} type="submit">
            {busy ? "Working…" : mode === "login" ? "Sign in" : "Create organization"}
          </button>
        </form>

        <p className="text-dim" style={{ fontSize: 12, marginTop: 16, textAlign: "center" }}>
          {mode === "login" ? "No account yet?" : "Already registered?"}{" "}
          <a
            href="#"
            onClick={(e) => {
              e.preventDefault();
              setMode(mode === "login" ? "register" : "login");
              setError(null);
            }}
            style={{ color: "var(--accent)" }}
          >
            {mode === "login" ? "Create one" : "Sign in"}
          </a>
        </p>
      </div>
    </main>
  );
}
