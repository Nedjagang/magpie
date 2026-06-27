"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { API, api, auth, AuthRequiredError, RolloutInFlightError, RolloutValidateError, type Agent, type ApplyAggregate, type ApplyState, type AuditChainStatus, type AuditEntry, type AuditFilter, type Config, type ReleaseCatalog, type ReleasePlatform, type Rollout, type RolloutWithAggregate } from "@/lib/api";
import { configStatusLabel, shortHash, sinceShort } from "@/lib/format";
import { templateFor, VARIANTS, type VariantKey } from "@/lib/templates";

type Grouped = Record<string, Record<string, Config[]>>;

function groupConfigs(configs: Config[]): Grouped {
  const out: Grouped = {};
  for (const c of configs) {
    const p = c.product || "default";
    const v = c.variant || "default";
    (out[p] ??= {})[v] ??= [];
    out[p][v].push(c);
  }
  return out;
}

function agentLabels(a: Agent): { product: string; variant: string } {
  return {
    product: a.attributes?.["magpie.product"] ?? "default",
    variant: a.attributes?.["magpie.variant"] ?? "default",
  };
}

// v1.0 IA shift (host-first, see docs/v1.0-fleet-view-spec.md): the sidebar
// leads with Fleet and demotes Products to a section below it. Rollouts and
// Audit are first-class. The audit kind carries an optional initialFilter
// so cross-link entries (e.g. host drawer's "View all events for this host")
// can pre-fill the filter chips on landing.
type Selection =
  | { kind: "fleet" }
  | { kind: "product"; product: string }
  | { kind: "rollouts" }
  | { kind: "audit"; initialFilter?: import("@/lib/api").AuditFilter };

// saveBlob triggers a "Save As" download for an in-memory Blob. We need
// this because /api/v1/releases/<os>/<arch> requires the bearer token in
// v0.2 — a plain <a href> doesn't carry headers, so the binary has to
// flow through fetch+Blob first. Buffers the whole zip in browser memory
// (~10 MB), which is fine at agent-binary scale.
function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export default function Home() {
  const [agents, setAgents] = useState<Agent[] | null>(null);
  const [configs, setConfigs] = useState<Config[] | null>(null);
  const [audit, setAudit] = useState<AuditEntry[] | null>(null);
  const [rollouts, setRollouts] = useState<Rollout[] | null>(null);
  const [health, setHealth] = useState<{ status: string; version: string } | null>(null);
  const [, setTick] = useState(0);

  const [selected, setSelected] = useState<Selection | null>(null);
  const [editor, setEditor] = useState<{ product: string; variant: string } | null>(null);
  const [onboard, setOnboard] = useState<{ product: string; variant: string } | null>(null);
  const [newProductOpen, setNewProductOpen] = useState(false);
  const [agentDetail, setAgentDetail] = useState<Agent | null>(null);
  const [rolloutDetail, setRolloutDetail] = useState<Rollout | null>(null);
  const [signInOpen, setSignInOpen] = useState(false);

  const refresh = useCallback(async () => {
    try {
      // Rollouts are fetched without a state filter so the Rollouts list
      // can show recent terminal rollouts alongside in-flight ones; the
      // Fleet view's in-flight strip filters client-side. At v1.0 scale
      // (recent rollouts ≤ a few dozen) the extra rows are trivial.
      const [a, c, h, au, rs] = await Promise.all([
        api.agents(), api.configs(), api.health(), api.audit(50), api.listRollouts(),
      ]);
      setAgents(a);
      setConfigs(c);
      setHealth(h);
      setAudit(au);
      setRollouts(rs);
      setSignInOpen(false);
    } catch (e) {
      if (e instanceof AuthRequiredError) {
        // Surface the modal once; the polling loop keeps trying so the
        // modal closes automatically the moment a valid token is saved.
        setSignInOpen(true);
        return;
      }
      /* transient — leave whatever we last rendered */
    }
  }, []);

  useEffect(() => {
    refresh();
    const dataTimer = setInterval(refresh, 2500);
    const clockTimer = setInterval(() => setTick((t) => t + 1), 1000);
    return () => {
      clearInterval(dataTimer);
      clearInterval(clockTimer);
    };
  }, [refresh]);

  const grouped = useMemo(() => (configs ? groupConfigs(configs) : {}), [configs]);
  const products = useMemo(() => {
    const fromConfigs = Object.keys(grouped);
    const fromAgents = (agents ?? []).map((a) => agentLabels(a).product);
    return Array.from(new Set([...fromConfigs, ...fromAgents])).sort();
  }, [grouped, agents]);

  // v1.0 host-first IA: default landing is Fleet (per spec §2). Operators
  // open the tab and the first thing they see is the fleet, not whichever
  // product was alphabetically first. The previous "default to first
  // product" behavior was the v0.2 cohort-first leftover.
  useEffect(() => {
    if (selected === null) setSelected({ kind: "fleet" });
  }, [selected]);

  return (
    <div className="min-h-screen flex flex-col">
      <MastHead
        connected={health !== null}
        version={health?.version}
        agents={agents?.length ?? 0}
        healthy={agents?.filter((a) => a.healthy).length ?? 0}
      />

      <div className="flex-1 flex min-h-0">
        <Sidebar
          products={products}
          agents={agents ?? []}
          selected={selected}
          onSelect={setSelected}
          onNewProduct={() => setNewProductOpen(true)}
        />

        <main className="flex-1 overflow-y-auto bg-[var(--color-paper)]">
          <div className="mx-auto max-w-[1100px] px-8 py-10">
            {selected?.kind === "fleet" ? (
              <FleetView
                agents={agents}
                grouped={grouped}
                rollouts={rollouts ?? []}
                onHost={(a) => setAgentDetail(a)}
                onRollout={(r) => setRolloutDetail(r)}
                onChanged={() => { refresh(); }}
              />
            ) : selected?.kind === "product" ? (
              <ProductView
                product={selected.product}
                grouped={grouped}
                agents={agents ?? []}
                audit={audit ?? []}
                onEdit={(variant) => setEditor({ product: selected.product, variant })}
                onOnboard={(variant) => setOnboard({ product: selected.product, variant })}
                onHost={(a) => setAgentDetail(a)}
                onDeleteProduct={async () => {
                  if (!confirm(`Delete ALL configs for "${selected.product}"?\n\nAgents on this product will fall back to the default/default config (or nothing if that's missing). This cannot be undone.`)) return;
                  await api.deleteProduct(selected.product);
                  setSelected({ kind: "fleet" });
                  refresh();
                }}
                onDeleteVariant={async (variant) => {
                  if (!confirm(`Delete config history for "${selected.product} · ${variant}"?\n\nMatching agents will fall back to the default/default config. This cannot be undone.`)) return;
                  await api.deleteVariant(selected.product, variant);
                  refresh();
                }}
              />
            ) : selected?.kind === "rollouts" ? (
              <RolloutsView
                rollouts={rollouts}
                onRollout={(r) => setRolloutDetail(r)}
              />
            ) : selected?.kind === "audit" ? (
              <AuditView
                initialFilter={selected.initialFilter}
                onHost={(uid) => {
                  const agent = (agents ?? []).find((a) => a.instance_uid === uid);
                  if (agent) setAgentDetail(agent);
                }}
                onProduct={(product) => setSelected({ kind: "product", product })}
                onRollout={(id) => {
                  const r = (rollouts ?? []).find((x) => x.id === id);
                  if (r) setRolloutDetail(r);
                }}
              />
            ) : (
              <div className="pt-32 text-center text-[var(--color-muted)]">
                {agents === null ? "Connecting…" : "Loading…"}
              </div>
            )}
          </div>
        </main>
      </div>

      {editor ? (
        <EditorDrawer
          product={editor.product}
          variant={editor.variant}
          revisions={grouped[editor.product]?.[editor.variant] ?? []}
          onClose={() => setEditor(null)}
          onChanged={refresh}
        />
      ) : null}

      {onboard ? (
        <OnboardModal
          target={onboard}
          onClose={() => setOnboard(null)}
          suggestedVariants={onboard.product ? Object.keys(grouped[onboard.product] ?? {}) : []}
          onPickVariant={(v) => setOnboard({ ...onboard, variant: v })}
        />
      ) : null}

      {agentDetail ? (
        <AgentDetailDrawer
          agent={agentDetail}
          products={products}
          grouped={grouped}
          rollouts={rollouts}
          controlPlaneVersion={health?.version ?? null}
          onClose={() => setAgentDetail(null)}
          onChanged={() => { refresh(); }}
          onOpenProduct={(product) => {
            setAgentDetail(null);
            setSelected({ kind: "product", product });
          }}
          onOpenRollout={(rollout) => {
            setAgentDetail(null);
            setRolloutDetail(rollout);
          }}
          onViewFullAudit={(hostUid) => {
            setAgentDetail(null);
            setSelected({ kind: "audit", initialFilter: { host_ref: hostUid, since: "30d" } });
          }}
        />
      ) : null}

      {rolloutDetail ? (
        <RolloutDrawer
          rolloutId={rolloutDetail.id}
          agents={agents ?? []}
          onClose={() => setRolloutDetail(null)}
          onChanged={() => { refresh(); }}
          onHost={(uid) => {
            const a = (agents ?? []).find((x) => x.instance_uid === uid);
            if (a) setAgentDetail(a);
          }}
        />
      ) : null}

      {newProductOpen ? (
        <NewProductModal
          onClose={() => setNewProductOpen(false)}
          onCreated={(product, variant) => {
            setNewProductOpen(false);
            setSelected({ kind: "product", product });
            setEditor({ product, variant });
          }}
        />
      ) : null}

      {signInOpen ? (
        <SignInModal
          onSignedIn={() => {
            // refresh() drives the close on success — kicking it now means
            // the modal disappears on the next poll tick (≤ 2.5s) without
            // needing to wait. Failure leaves the modal in place.
            void refresh();
          }}
        />
      ) : null}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Sign-in modal — surfaces when magpied returns 401 (i.e. MAGPIE_API_TOKEN
// is set on the server but the browser has no/wrong token in localStorage).
// ─────────────────────────────────────────────────────────────────────────────

function SignInModal({ onSignedIn }: { onSignedIn: () => void }) {
  const [token, setToken] = useState("");
  const [actor, setActor] = useState(auth.getActor());
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    auth.setToken(token.trim());
    auth.setActor(actor.trim());
    try {
      // Probe the token against /api/v1/agents — cheapest authed endpoint.
      // If it 401s, the AuthRequiredError surfaces here and we tell the
      // operator the token didn't take.
      await api.agents();
      onSignedIn();
    } catch (e) {
      if (e instanceof AuthRequiredError) {
        setErr("Token rejected by magpied.");
      } else {
        setErr(e instanceof Error ? e.message : "sign-in failed");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm">
      <form
        onSubmit={submit}
        className="card w-full max-w-md mx-4 px-6 py-6"
        style={{ background: "var(--color-card)" }}
      >
        <div className="eyebrow mb-2">Sign in</div>
        <h2 className="text-[20px] font-light leading-tight mb-1" style={{ fontFamily: "var(--font-serif)" }}>
          API token required
        </h2>
        <p className="hint" style={{ marginTop: 4 }}>
          This control plane has <span className="code">MAGPIE_API_TOKEN</span> set. Paste the token to continue.
        </p>
        <label className="block mt-4">
          <span className="eyebrow">Bearer token</span>
          <input
            type="password"
            autoFocus
            spellCheck={false}
            autoComplete="off"
            className="field mt-1.5 code w-full"
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
        </label>
        <label className="block mt-3">
          <span className="eyebrow">Display name (optional, for audit log)</span>
          <input
            type="text"
            spellCheck={false}
            className="field mt-1.5 w-full"
            value={actor}
            onChange={(e) => setActor(e.target.value)}
            placeholder="e.g. alice@corp"
          />
        </label>
        {err ? <div className="text-xs text-[var(--color-danger)] mt-3">{err}</div> : null}
        <div className="mt-5 flex justify-end gap-2">
          <button type="submit" className="btn" disabled={busy || !token.trim()}>
            {busy ? "Verifying…" : "Sign in"}
          </button>
        </div>
        <p className="hint mt-4" style={{ fontSize: 11 }}>
          Token is held in <span className="code">localStorage</span>. To clear, sign out from the masthead.
        </p>
      </form>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Masthead
// ─────────────────────────────────────────────────────────────────────────────

function MastHead({
  connected,
  version,
  agents,
  healthy,
}: {
  connected: boolean;
  version?: string;
  agents: number;
  healthy: number;
}) {
  // The Sign-out button's visibility depends on auth.getToken(), which
  // reads from localStorage — only available client-side. Rendering it
  // directly causes a hydration mismatch (SSR has no token, client does).
  // Gate on a post-mount flag so SSR and the initial hydration render
  // the same tree; the button appears on the next render.
  const [mounted, setMounted] = useState(false);
  useEffect(() => { setMounted(true); }, []);
  const showSignOut = mounted && Boolean(auth.getToken());
  return (
    <header className="border-b border-[var(--color-rule)] bg-[var(--color-card)]">
      <div className="mx-auto max-w-[1400px] px-8 h-16 flex items-center justify-between">
        <div className="flex items-baseline gap-3">
          <h1
            className="text-[24px] font-light tracking-tight leading-none"
            style={{ fontFamily: "var(--font-serif)" }}
          >
            magpie
          </h1>
          <span className="text-[var(--color-muted-soft)] text-xs">control plane</span>
          {version ? <span className="code text-[var(--color-muted-soft)] text-[11px]">{version}</span> : null}
        </div>

        <div className="flex items-center gap-4">
          <span className="text-xs text-[var(--color-muted)]">
            {healthy} / {agents} healthy
          </span>
          <span className={connected ? "pill pill-ok" : "pill pill-bad"}>
            <span
              aria-hidden
              className={connected ? "breathe" : ""}
              style={{
                width: 6, height: 6, borderRadius: 999,
                background: connected ? "var(--color-accent)" : "var(--color-danger)",
                display: "inline-block",
              }}
            />
            {connected ? "Live" : "Offline"}
          </span>
          {showSignOut ? (
            <button
              type="button"
              className="btn-ghost text-xs"
              onClick={() => {
                auth.setToken("");
                // Forces a 401 on the next poll, which surfaces the sign-in modal.
                window.location.reload();
              }}
              title="Clear API token from this browser"
            >
              Sign out
            </button>
          ) : null}
        </div>
      </div>
    </header>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Sidebar
// ─────────────────────────────────────────────────────────────────────────────

function Sidebar({
  products,
  agents,
  selected,
  onSelect,
  onNewProduct,
}: {
  products: string[];
  agents: Agent[];
  selected: Selection | null;
  onSelect: (s: Selection) => void;
  onNewProduct: () => void;
}) {
  // Use effective_product (post-override) so a host with a label override
  // shows up under the cohort that's actually serving its config — matches
  // the v1.0 host drawer's Scope-resolution chain. Falls back to advertised
  // labels when the API hasn't yet populated the effective fields (e.g. an
  // agent that's pending its first heartbeat).
  const countBy = (p: string) =>
    agents.filter((a) => (a.effective_product ?? agentLabels(a).product) === p).length;

  return (
    <aside className="w-[240px] shrink-0 border-r border-[var(--color-rule)] bg-[var(--color-card)] py-6 px-4 overflow-y-auto">
      {/* Fleet — host-first IA default landing. */}
      <nav className="flex flex-col gap-0.5">
        <button
          className="nav-item"
          aria-current={selected?.kind === "fleet"}
          onClick={() => onSelect({ kind: "fleet" })}
        >
          <span>Fleet</span>
          <span className="nav-count">{agents.length}</span>
        </button>
      </nav>

      <div className="h-px bg-[var(--color-rule-soft)] my-4" />

      {/* Products — cohorts where authoring lives. */}
      <div className="eyebrow px-2 mb-2">Products</div>
      <nav className="flex flex-col gap-0.5">
        {products.length === 0 ? (
          <div className="px-2 text-xs text-[var(--color-muted-soft)]">No products yet.</div>
        ) : (
          products.map((p) => {
            const isActive = selected?.kind === "product" && selected.product === p;
            return (
              <button
                key={p}
                className="nav-item"
                aria-current={isActive}
                onClick={() => onSelect({ kind: "product", product: p })}
              >
                <span className="truncate">{p}</span>
                <span className="nav-count">{countBy(p)}</span>
              </button>
            );
          })
        )}
        <button className="nav-item text-[var(--color-muted)]" onClick={onNewProduct}>
          <span>+ New product</span>
        </button>
      </nav>

      <div className="h-px bg-[var(--color-rule-soft)] my-4" />

      {/* Rollouts + Audit — first-class v1.0 sections. UI panes pending,
          but the navigation shape signals the v1.0 IA already. */}
      <nav className="flex flex-col gap-0.5">
        <button
          className="nav-item"
          aria-current={selected?.kind === "rollouts"}
          onClick={() => onSelect({ kind: "rollouts" })}
        >
          <span>Rollouts</span>
        </button>
        <button
          className="nav-item"
          aria-current={selected?.kind === "audit"}
          onClick={() => onSelect({ kind: "audit" })}
        >
          <span>Audit</span>
        </button>
      </nav>
    </aside>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Product detail
// ─────────────────────────────────────────────────────────────────────────────

function ProductView({
  product,
  grouped,
  agents,
  audit,
  onEdit,
  onOnboard,
  onHost,
  onDeleteProduct,
  onDeleteVariant,
}: {
  product: string;
  grouped: Grouped;
  agents: Agent[];
  audit: AuditEntry[];
  onEdit: (variant: string) => void;
  onOnboard: (variant: string) => void;
  onHost: (a: Agent) => void;
  onDeleteProduct: () => void;
  onDeleteVariant: (variant: string) => void;
}) {
  const productAgents = agents.filter((a) => agentLabels(a).product === product);
  const healthyCount = productAgents.filter((a) => a.healthy).length;
  const variants = grouped[product] ?? {};
  const variantKeys = Array.from(
    new Set([
      ...Object.keys(variants),
      ...productAgents.map((a) => agentLabels(a).variant),
    ])
  ).sort();

  const productAudit = audit.filter((e) => e.product === product).slice(0, 5);

  return (
    <div>
      <header className="mb-8 flex items-start justify-between gap-4">
        <div>
          <h2
            className="text-[30px] font-light tracking-tight leading-none"
            style={{ fontFamily: "var(--font-serif)" }}
          >
            {product}
          </h2>
          <p className="mt-2 text-sm text-[var(--color-muted)]">
            {productAgents.length} {productAgents.length === 1 ? "agent" : "agents"} · {healthyCount} healthy
          </p>
        </div>
        <button type="button" className="btn-danger" onClick={onDeleteProduct}>
          Delete product
        </button>
      </header>

      <section>
        <div className="flex items-baseline justify-between mb-3">
          <span className="eyebrow">Variants</span>
          <button
            type="button"
            className="btn-ghost"
            onClick={() => onEdit("__new__")}
            title="Add a new variant (linux, windows, k8s, or custom)"
          >
            + New variant
          </button>
        </div>

        {variantKeys.length === 0 ? (
          <div className="card px-6 py-8 text-sm text-[var(--color-muted)]">
            No variants published yet for <span className="code">{product}</span>.{" "}
            <button className="underline" onClick={() => onEdit("__new__")}>Publish the first one</button>.
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {variantKeys.map((v) => (
              <VariantRow
                key={v}
                variant={v}
                active={variants[v]?.[0]}
                agents={productAgents.filter((a) => agentLabels(a).variant === v)}
                onEdit={() => onEdit(v)}
                onOnboard={() => onOnboard(v)}
                onDelete={() => onDeleteVariant(v)}
              />
            ))}
          </div>
        )}
      </section>

      <section className="mt-12">
        <div className="flex items-baseline justify-between mb-3">
          <span className="eyebrow">Hosts on {product}</span>
          <span className="text-xs text-[var(--color-muted)]">{productAgents.length}</span>
        </div>
        <div className="card overflow-hidden">
          {productAgents.length === 0 ? (
            <div className="px-5 py-8 text-sm text-[var(--color-muted)]">
              No agents yet.{" "}
              <button className="underline" onClick={() => onOnboard(variantKeys[0] ?? "linux")}>
                Onboard one
              </button>
              .
            </div>
          ) : (
            <HostTable agents={productAgents} variants={variants} onRow={onHost} />
          )}
        </div>
      </section>

      <section className="mt-12">
        <div className="flex items-baseline justify-between mb-3">
          <span className="eyebrow">Recent activity</span>
        </div>
        {productAudit.length === 0 ? (
          <div className="text-xs text-[var(--color-muted-soft)]">Nothing yet.</div>
        ) : (
          <ul className="card divide-y divide-[var(--color-rule-soft)]">
            {productAudit.map((e) => (
              <li key={e.id} className="px-5 py-2.5 text-sm flex items-baseline justify-between gap-4">
                <span className="truncate">
                  <span className="code text-xs text-[var(--color-muted)] mr-2">{e.action}</span>
                  {e.detail}
                </span>
                <span className="text-xs text-[var(--color-muted-soft)] whitespace-nowrap">
                  {e.actor} · {sinceShort(e.at)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function VariantRow({
  variant,
  active,
  agents,
  onEdit,
  onOnboard,
  onDelete,
}: {
  variant: string;
  active?: Config;
  agents: Agent[];
  onEdit: () => void;
  onOnboard: () => void;
  onDelete: () => void;
}) {
  const healthy = agents.filter((a) => a.healthy).length;
  return (
    <div className="card px-5 py-4 flex items-center gap-5">
      <div className="w-[110px] shrink-0">
        <div className="font-medium text-[15px]">{variant}</div>
        <div className="text-xs text-[var(--color-muted)] mt-0.5">
          {agents.length} {agents.length === 1 ? "agent" : "agents"}
          {agents.length > 0 && healthy < agents.length ? (
            <span className="text-[var(--color-danger)]"> · {agents.length - healthy} down</span>
          ) : null}
        </div>
      </div>

      <div className="flex-1 min-w-0">
        {active ? (
          <>
            <div className="text-sm truncate">{active.name}</div>
            <div className="text-xs text-[var(--color-muted)] mt-0.5">
              <span className="code">{shortHash(hashFor(active.yaml))}</span>
              <span className="ml-2">updated {sinceShort(active.created_at)}</span>
            </div>
          </>
        ) : (
          <div className="text-sm text-[var(--color-muted)] italic">no config yet</div>
        )}
      </div>

      <div className="flex items-center gap-2 shrink-0">
        <button type="button" className="btn-ghost" onClick={onOnboard}>Onboard</button>
        <button type="button" className="btn" onClick={onEdit}>Edit ▸</button>
        {active ? (
          <button type="button" className="btn-danger" onClick={onDelete} title="Delete this variant's config history">
            ✕
          </button>
        ) : null}
      </div>
    </div>
  );
}

// Pseudo-hash display — the server-computed hash is what agents see, but for
// UI purposes a cheap JS fnv1a is fine so we don't round-trip.
function hashFor(y: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < y.length; i++) {
    h ^= y.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0");
}

function HostTable({ agents, variants, onRow }: { agents: Agent[]; variants: Record<string, Config[]>; onRow: (a: Agent) => void }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left bg-[var(--color-paper-warm)] border-b border-[var(--color-rule)]">
          <Th>Host</Th>
          <Th>Variant</Th>
          <Th className="hidden md:table-cell">Running</Th>
          <Th>Status</Th>
          <Th className="text-right">Last seen</Th>
        </tr>
      </thead>
      <tbody>
        {agents.map((a) => {
          const { variant } = agentLabels(a);
          const host = a.attributes?.["host.name"] ?? a.attributes?.["service.instance.id"] ?? "unknown";
          const status = configStatusLabel(a.config_status);
          const active = variants[variant]?.[0];
          return (
            <tr
              key={a.instance_uid}
              className="border-b border-[var(--color-rule-soft)] last:border-0 cursor-pointer hover:bg-[var(--color-paper-warm)]/50"
              onClick={() => onRow(a)}
            >
              <td className="px-5 py-3">
                <span
                  aria-label={a.healthy ? "healthy" : "unhealthy"}
                  style={{
                    width: 7, height: 7, borderRadius: 999,
                    background: a.healthy ? "var(--color-accent)" : "var(--color-danger)",
                    display: "inline-block", marginRight: 8,
                  }}
                />
                <span className="font-medium">{host}</span>
              </td>
              <td className="px-5 py-3 code text-[var(--color-muted)]">{variant}</td>
              <td className="hidden md:table-cell px-5 py-3 text-[var(--color-muted)]">
                {active?.name ?? "—"}
              </td>
              <td className="px-5 py-3">
                <span className={status === "applied" ? "pill pill-ok" : status === "failed" ? "pill pill-bad" : "pill"}>{status}</span>
              </td>
              <td className="px-5 py-3 text-right text-[var(--color-muted)] whitespace-nowrap">{sinceShort(a.last_seen)}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Fleet view — v1.0 host-first IA default landing. See
// docs/v1.0-fleet-view-spec.md for the full design. This first slice ships
// the structural pieces: status strip + filter bar + host table. The
// in-flight rollouts strip, multi-select bulk actions, virtualization, and
// URL-serializable filter state come in follow-up turns; their absence
// doesn't break behavior at v0.2 fleet sizes (the table just renders the
// loaded page directly).
// ─────────────────────────────────────────────────────────────────────────────

type FleetStatusFilter = "" | "healthy" | "unhealthy" | "failed";

function FleetView({
  agents,
  grouped,
  rollouts,
  onHost,
  onRollout,
  onChanged,
}: {
  agents: Agent[] | null;
  grouped: Grouped;
  rollouts: Rollout[];
  onHost: (a: Agent) => void;
  onRollout: (r: Rollout) => void;
  onChanged: () => void;
}) {
  const [q, setQ] = useState("");
  const [statusFilter, setStatusFilter] = useState<FleetStatusFilter>("");

  // v1.0 multi-select per spec §8. First slice ships visible-selection
  // (header checkbox toggles all currently-filtered rows). The "Select
  // all 1,840 matching filter" upgrade banner from spec §8.1 is deferred
  // until cursor pagination teaches the UI to materialise rows beyond
  // the loaded set.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkOpen, setBulkOpen] = useState<null | "delete" | "relabel">(null);

  const all = agents ?? [];

  // In-flight rollouts strip per spec §5: hidden when zero, horizontal
  // when 1+. Filter client-side from the page-level rollouts state.
  const inFlight = useMemo(
    () => rollouts.filter((r) => r.state !== "done" && r.state !== "aborted"),
    [rollouts],
  );

  // Status strip metrics, derived client-side from the loaded fleet. At
  // v1.0 scale (≤ 500-row default page), this is microseconds; at full
  // 2000-host scale the planned aggregate-stats endpoint takes over.
  const totals = useMemo(() => {
    let healthy = 0, unhealthy = 0, failed = 0;
    for (const a of all) {
      if (a.config_status === "failed") failed++;
      if (a.healthy === true) healthy++;
      else if (a.healthy === false) unhealthy++;
    }
    return { total: all.length, healthy, unhealthy, failed };
  }, [all]);

  // Filter pipeline: status chip first (cheaper, narrows the set), then
  // text search across host/product/variant. Both client-side for the
  // v1.0 first slice — server-side ?q= is a v1.x increment.
  const filtered = useMemo(() => {
    return all.filter((a) => {
      if (statusFilter === "healthy" && a.healthy !== true) return false;
      if (statusFilter === "unhealthy" && a.healthy !== false) return false;
      if (statusFilter === "failed" && a.config_status !== "failed") return false;
      if (q) {
        const host = a.attributes?.["host.name"] ?? "";
        const { product, variant } = agentLabels(a);
        if (!(host + " " + product + " " + variant).toLowerCase().includes(q.toLowerCase())) {
          return false;
        }
      }
      return true;
    });
  }, [all, statusFilter, q]);

  // Selection-derived state: which selected rows are still visible after
  // filtering (operator changing filter mid-selection shouldn't act on
  // rows they can no longer see), what cohorts the selection spans, and
  // the cross-product Edit-config gate from spec §9.
  const selectedAgents = useMemo(
    () => filtered.filter((a) => selected.has(a.instance_uid)),
    [filtered, selected],
  );
  const selectedProducts = useMemo(() => {
    const ps = new Set<string>();
    for (const a of selectedAgents) ps.add(a.effective_product ?? agentLabels(a).product);
    return ps;
  }, [selectedAgents]);
  const allFilteredSelected = filtered.length > 0 && selectedAgents.length === filtered.length;

  function toggleAll() {
    if (allFilteredSelected) {
      setSelected(new Set());
    } else {
      setSelected(new Set(filtered.map((a) => a.instance_uid)));
    }
  }
  function toggleOne(uid: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(uid)) next.delete(uid);
      else next.add(uid);
      return next;
    });
  }

  return (
    <div>
      <header className="mb-6 flex items-baseline justify-between gap-4">
        <h2 className="text-[30px] font-light tracking-tight leading-none" style={{ fontFamily: "var(--font-serif)" }}>
          Fleet
        </h2>
      </header>

      {/* Status strip — five-card design from spec §4 (drift card deferred
          until rollout-driven live-config resolution lights up; the four
          here are the load-bearing "is anything on fire?" signals). Each
          card is click-filterable; click toggles. */}
      <section className="mb-6 grid grid-cols-2 md:grid-cols-4 gap-3">
        <StatusCard
          label="hosts total"
          value={totals.total}
          active={statusFilter === ""}
          onClick={() => setStatusFilter("")}
          tone="neutral"
        />
        <StatusCard
          label="healthy"
          value={totals.healthy}
          active={statusFilter === "healthy"}
          onClick={() => setStatusFilter(statusFilter === "healthy" ? "" : "healthy")}
          tone={totals.healthy > 0 ? "ok" : "neutral"}
        />
        <StatusCard
          label="unhealthy"
          value={totals.unhealthy}
          active={statusFilter === "unhealthy"}
          onClick={() => setStatusFilter(statusFilter === "unhealthy" ? "" : "unhealthy")}
          tone={totals.unhealthy > 0 ? "warn" : "neutral"}
        />
        <StatusCard
          label="failed apply"
          value={totals.failed}
          active={statusFilter === "failed"}
          onClick={() => setStatusFilter(statusFilter === "failed" ? "" : "failed")}
          tone={totals.failed > 0 ? "bad" : "neutral"}
        />
      </section>

      {/* In-flight rollouts strip — spec §5. Hidden when zero in-flight;
          horizontal scroll past 4 cards. Clicking a card opens the
          Rollouts drawer. Lights up the pane that was a placeholder in
          Fleet's first slice. */}
      {inFlight.length > 0 ? (
        <section className="mb-6">
          <div className="flex items-baseline justify-between mb-2">
            <span className="eyebrow">In-flight rollouts ({inFlight.length})</span>
            {inFlight.length > 4 ? (
              <span className="text-xs text-[var(--color-muted)]">
                Scroll for more →
              </span>
            ) : null}
          </div>
          <div className="flex gap-2 overflow-x-auto pb-1">
            {inFlight.map((r) => (
              <RolloutCard key={r.id} r={r} onClick={() => onRollout(r)} />
            ))}
          </div>
        </section>
      ) : null}

      {/* Filter bar — search + clear-all. Cohort and last-seen chips arrive
          alongside server-side filtering in a follow-up. */}
      <section className="mb-4 flex items-center gap-3 flex-wrap">
        <input
          className="field flex-1 min-w-[240px] max-w-[420px]"
          placeholder="Search host, product, variant…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          spellCheck={false}
        />
        <span className="text-xs text-[var(--color-muted)]">
          Showing {filtered.length} of {all.length}
        </span>
        {(q || statusFilter) ? (
          <button
            type="button"
            className="btn-ghost text-xs"
            onClick={() => { setQ(""); setStatusFilter(""); }}
          >
            Clear filters
          </button>
        ) : null}
      </section>

      {/* Bulk action bar — sticky above the table when ≥1 row is selected.
          Cross-product Edit-config guard per spec §9: button disabled
          with explanation when selection spans multiple products. The
          modal-pick variant (multiple cohorts in one product) is polish;
          v1.0 first slice does enabled / disabled-with-tooltip. */}
      {selectedAgents.length > 0 ? (
        <section className="mb-4 card px-4 py-3 flex items-center justify-between gap-3 flex-wrap">
          <div className="text-sm">
            <span className="font-medium">{selectedAgents.length} {selectedAgents.length === 1 ? "host" : "hosts"} selected</span>
            {selectedProducts.size > 1 ? (
              <span className="text-xs text-[var(--color-muted)] ml-2">
                across {selectedProducts.size} products
              </span>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button" className="btn-ghost"
              onClick={() => setBulkOpen("relabel")}
            >
              Re-assign cohort
            </button>
            <button
              type="button" className="btn-ghost"
              disabled={selectedProducts.size > 1}
              title={selectedProducts.size > 1
                ? `These ${selectedAgents.length} hosts span ${selectedProducts.size} products. Magpie publishes to a single (product, variant) at a time. Narrow your selection or use per-product Publish.`
                : "Edit cohort config (opens publish dialog)"}
            >
              Edit config
            </button>
            <button
              type="button" className="btn-danger"
              onClick={() => setBulkOpen("delete")}
            >
              Delete
            </button>
            <button
              type="button" className="btn-ghost"
              onClick={() => setSelected(new Set())}
            >
              Cancel
            </button>
          </div>
        </section>
      ) : null}

      <div className="card overflow-hidden">
        {agents === null ? (
          <div className="px-5 py-8 text-sm text-[var(--color-muted)]">Connecting…</div>
        ) : filtered.length === 0 ? (
          <div className="px-5 py-8 text-sm text-[var(--color-muted)]">
            {all.length === 0 ? "No hosts yet. Onboard one from a product page." : "No matches."}
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left bg-[var(--color-paper-warm)] border-b border-[var(--color-rule)]">
                <th className="px-3 py-2.5 w-8" style={{ fontSize: 10.5 }}>
                  <input
                    type="checkbox"
                    aria-label={allFilteredSelected ? "Deselect all visible" : "Select all visible"}
                    checked={allFilteredSelected}
                    onChange={toggleAll}
                  />
                </th>
                <Th>Host</Th>
                <Th>Product · Variant</Th>
                <Th className="hidden md:table-cell">Running</Th>
                <Th>Status</Th>
                <Th className="text-right">Last seen</Th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((a) => {
                const { product, variant } = agentLabels(a);
                const host = a.attributes?.["host.name"] ?? "unknown";
                const status = configStatusLabel(a.config_status);
                const active = grouped[product]?.[variant]?.[0];
                const isSelected = selected.has(a.instance_uid);
                return (
                  <tr
                    key={a.instance_uid}
                    className="border-b border-[var(--color-rule-soft)] last:border-0 cursor-pointer hover:bg-[var(--color-paper-warm)]/50"
                    style={isSelected ? { background: "var(--color-paper-warm)" } : undefined}
                    onClick={() => onHost(a)}
                  >
                    <td
                      className="px-3 py-3 w-8"
                      onClick={(e) => { e.stopPropagation(); toggleOne(a.instance_uid); }}
                    >
                      <input
                        type="checkbox"
                        aria-label={`Select ${host}`}
                        checked={isSelected}
                        // Click handled at the <td> level so the row's
                        // drawer-open onClick doesn't fire — propagation
                        // is stopped above.
                        onChange={() => { /* delegated to <td> */ }}
                      />
                    </td>
                    <td className="px-5 py-3">
                      <span
                        aria-label={a.healthy ? "healthy" : "unhealthy"}
                        style={{
                          width: 7, height: 7, borderRadius: 999,
                          background: a.healthy ? "var(--color-accent)" : "var(--color-danger)",
                          display: "inline-block", marginRight: 8,
                        }}
                      />
                      <span className="font-medium">{host}</span>
                    </td>
                    <td className="px-5 py-3 code text-[var(--color-muted)]">{product} · {variant}</td>
                    <td className="hidden md:table-cell px-5 py-3 text-[var(--color-muted)]">{active?.name ?? "—"}</td>
                    <td className="px-5 py-3">
                      <span className={status === "applied" ? "pill pill-ok" : status === "failed" ? "pill pill-bad" : "pill"}>{status}</span>
                    </td>
                    <td className="px-5 py-3 text-right text-[var(--color-muted)] whitespace-nowrap">{sinceShort(a.last_seen)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {bulkOpen === "delete" ? (
        <BulkDeleteConfirm
          agents={selectedAgents}
          onClose={() => setBulkOpen(null)}
          onDone={() => {
            setBulkOpen(null);
            setSelected(new Set());
            onChanged();
          }}
        />
      ) : null}
      {bulkOpen === "relabel" ? (
        <BulkRelabelModal
          agents={selectedAgents}
          onClose={() => setBulkOpen(null)}
          onDone={() => {
            setBulkOpen(null);
            setSelected(new Set());
            onChanged();
          }}
        />
      ) : null}
    </div>
  );
}

// BulkDeleteConfirm — spec §8.3. Enumerates rows by host name + cohort
// before commit. At larger scale shows top 20 + cohort summary; the
// confirm button names the count explicitly (operator sees "Delete N
// hosts" not just "Delete"). Healthy-host warning per spec §5 reliability
// guard #6: warn but don't block — agents re-register on heartbeat.
function BulkDeleteConfirm({
  agents,
  onClose,
  onDone,
}: {
  agents: Agent[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [pending, setPending] = useState(false);
  const [errors, setErrors] = useState<{ uid: string; reason: string }[]>([]);

  // Cohort breakdown — spec §8.3 large-N case. Always rendered; useful
  // even at small N because it surfaces cross-product spillover at a glance.
  const cohorts = useMemo(() => {
    const m = new Map<string, number>();
    for (const a of agents) {
      const k = `${a.effective_product ?? agentLabels(a).product}/${a.effective_variant ?? agentLabels(a).variant}`;
      m.set(k, (m.get(k) ?? 0) + 1);
    }
    return Array.from(m.entries()).sort((a, b) => b[1] - a[1]);
  }, [agents]);

  // "Recently healthy" warning per spec §5: last_seen < 5 min ago.
  const recentlyHealthy = useMemo(() => {
    const cutoff = Date.now() - 5 * 60 * 1000;
    return agents.filter((a) => a.healthy && Date.parse(a.last_seen) > cutoff);
  }, [agents]);

  async function commit() {
    setPending(true);
    setErrors([]);
    const fails: { uid: string; reason: string }[] = [];
    // Spec §8.4: N parallel deletes for v1.0; bulk endpoint deferred.
    // Promise.allSettled so one failure doesn't abort the rest.
    const results = await Promise.allSettled(agents.map((a) => api.deleteAgent(a.instance_uid)));
    results.forEach((r, i) => {
      if (r.status === "rejected") {
        fails.push({
          uid: agents[i].instance_uid,
          reason: r.reason instanceof Error ? r.reason.message : String(r.reason),
        });
      }
    });
    if (fails.length > 0) {
      setErrors(fails);
      setPending(false);
      return;
    }
    onDone();
  }

  const top = agents.slice(0, 20);
  const more = agents.length - top.length;

  return (
    <>
      <div className="modal-scrim" onClick={pending ? undefined : onClose} aria-hidden />
      <div role="dialog" aria-modal="true" className="modal-wrap" onClick={pending ? undefined : onClose}>
        <div className="card p-6 md:p-7" style={{ maxWidth: 720, width: "100%" }} onClick={(e) => e.stopPropagation()}>
          <div className="eyebrow">Bulk delete</div>
          <h2 className="mt-1 mb-3 text-[22px] font-light tracking-tight" style={{ fontFamily: "var(--font-serif)" }}>
            Delete {agents.length} host {agents.length === 1 ? "record" : "records"}?
          </h2>

          {cohorts.length > 1 ? (
            <div className="mb-4">
              <div className="eyebrow mb-1">Across {cohorts.length} cohorts</div>
              <ul className="text-sm">
                {cohorts.map(([c, n]) => (
                  <li key={c}><span className="code">{c}</span> × {n}</li>
                ))}
              </ul>
            </div>
          ) : null}

          <div className="mb-4">
            <div className="eyebrow mb-1">{top.length === 1 ? "Host" : `First ${top.length} ${top.length === agents.length ? "" : "of " + agents.length}`}</div>
            <ul className="text-xs space-y-1 code max-h-[180px] overflow-y-auto">
              {top.map((a) => {
                const host = a.attributes?.["host.name"] ?? a.instance_uid.slice(0, 16);
                const cohort = `${a.effective_product ?? agentLabels(a).product}/${a.effective_variant ?? agentLabels(a).variant}`;
                const stale = sinceShort(a.last_seen);
                return (
                  <li key={a.instance_uid} className="flex items-baseline justify-between gap-3">
                    <span className="truncate">{host}</span>
                    <span className="text-[var(--color-muted)]">{cohort}</span>
                    <span className="text-[var(--color-muted-soft)] whitespace-nowrap">{stale}</span>
                  </li>
                );
              })}
            </ul>
            {more > 0 ? (
              <div className="text-xs text-[var(--color-muted)] mt-2">…and {more} more.</div>
            ) : null}
          </div>

          {recentlyHealthy.length > 0 ? (
            <div className="banner-notice mb-4">
              <strong>⚠ {recentlyHealthy.length} of these {recentlyHealthy.length === 1 ? "is" : "are"} currently healthy</strong> (last seen &lt; 5 min ago).
              Deleting will let the agent re-register on its next heartbeat — useful for clearing
              stale duplicates, harmless if the host is actually live.
            </div>
          ) : null}

          {errors.length > 0 ? (
            <div className="banner-error mb-4">
              <div className="eyebrow mb-1" style={{ color: "inherit" }}>{errors.length} delete{errors.length === 1 ? "" : "s"} failed</div>
              <ul className="code text-xs">
                {errors.slice(0, 5).map((e) => <li key={e.uid}>{e.uid.slice(0, 12)}: {e.reason}</li>)}
                {errors.length > 5 ? <li>…and {errors.length - 5} more.</li> : null}
              </ul>
            </div>
          ) : null}

          <div className="flex items-center justify-end gap-2">
            <button type="button" className="btn-ghost" onClick={onClose} disabled={pending}>
              Cancel
            </button>
            <button type="button" className="btn-danger" onClick={commit} disabled={pending}>
              {pending ? `Deleting ${agents.length}…` : `Delete ${agents.length} ${agents.length === 1 ? "host" : "hosts"}`}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

// BulkRelabelModal — spec §8.2. Operator picks a target product+variant,
// service applies the label override to every selected host. Allowed
// across products (per plan §5: bulk relabel is allowed across Products;
// only bulk-edit-config is gated). Uses Promise.allSettled to surface
// partial failures.
function BulkRelabelModal({
  agents,
  onClose,
  onDone,
}: {
  agents: Agent[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [product, setProduct] = useState("");
  const [variant, setVariant] = useState("");
  const [pending, setPending] = useState(false);
  const [errors, setErrors] = useState<{ uid: string; reason: string }[]>([]);

  async function commit() {
    if (!product.trim() || !variant.trim()) return;
    setPending(true);
    setErrors([]);
    const fails: { uid: string; reason: string }[] = [];
    const results = await Promise.allSettled(
      agents.map((a) => api.setAgentLabels(a.instance_uid, product.trim(), variant.trim())),
    );
    results.forEach((r, i) => {
      if (r.status === "rejected") {
        fails.push({
          uid: agents[i].instance_uid,
          reason: r.reason instanceof Error ? r.reason.message : String(r.reason),
        });
      }
    });
    if (fails.length > 0) {
      setErrors(fails);
      setPending(false);
      return;
    }
    onDone();
  }

  return (
    <>
      <div className="modal-scrim" onClick={pending ? undefined : onClose} aria-hidden />
      <div role="dialog" aria-modal="true" className="modal-wrap" onClick={pending ? undefined : onClose}>
        <div className="card p-6 md:p-7" style={{ maxWidth: 520, width: "100%" }} onClick={(e) => e.stopPropagation()}>
          <div className="eyebrow">Bulk re-assign cohort</div>
          <h2 className="mt-1 mb-3 text-[22px] font-light tracking-tight" style={{ fontFamily: "var(--font-serif)" }}>
            Re-assign {agents.length} {agents.length === 1 ? "host" : "hosts"}
          </h2>
          <p className="hint mb-4" style={{ marginTop: 0 }}>
            Writes a label override on each selected host. Hosts will pick up the
            target cohort&apos;s config on the next OpAMP heartbeat (~2 s).
          </p>

          <div className="grid grid-cols-2 gap-3 mb-4">
            <label className="block">
              <span className="eyebrow">Product</span>
              <input
                className="field mt-1.5 code" value={product}
                onChange={(e) => setProduct(e.target.value)}
                spellCheck={false} autoFocus
              />
            </label>
            <label className="block">
              <span className="eyebrow">Variant</span>
              <input
                className="field mt-1.5 code" value={variant}
                onChange={(e) => setVariant(e.target.value)}
                spellCheck={false}
              />
            </label>
          </div>

          {errors.length > 0 ? (
            <div className="banner-error mb-4">
              <div className="eyebrow mb-1" style={{ color: "inherit" }}>{errors.length} relabel{errors.length === 1 ? "" : "s"} failed</div>
              <ul className="code text-xs">
                {errors.slice(0, 5).map((e) => <li key={e.uid}>{e.uid.slice(0, 12)}: {e.reason}</li>)}
                {errors.length > 5 ? <li>…and {errors.length - 5} more.</li> : null}
              </ul>
            </div>
          ) : null}

          <div className="flex items-center justify-end gap-2">
            <button type="button" className="btn-ghost" onClick={onClose} disabled={pending}>
              Cancel
            </button>
            <button
              type="button" className="btn"
              disabled={pending || !product.trim() || !variant.trim()}
              onClick={commit}
            >
              {pending ? `Re-assigning ${agents.length}…` : `Re-assign ${agents.length} ${agents.length === 1 ? "host" : "hosts"}`}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

// StatusCard renders one of the five cards in the Fleet view's status
// strip. Click toggles the corresponding filter (passed in via onClick).
// "active" highlights the card whose filter is currently applied.
function StatusCard({
  label,
  value,
  active,
  onClick,
  tone,
}: {
  label: string;
  value: number;
  active: boolean;
  onClick: () => void;
  tone: "neutral" | "ok" | "warn" | "bad";
}) {
  const dot =
    tone === "ok"   ? "var(--color-accent)" :
    tone === "warn" ? "#caa247" :
    tone === "bad"  ? "var(--color-danger)" :
    "transparent";
  return (
    <button
      type="button"
      onClick={onClick}
      className="card text-left px-4 py-3 transition-colors hover:bg-[var(--color-paper-warm)]/40"
      style={{
        borderColor: active ? "var(--color-accent)" : undefined,
        background: active ? "var(--color-paper-warm)" : undefined,
      }}
      aria-pressed={active}
    >
      <div className="flex items-baseline gap-2">
        <span className="text-[24px] font-light leading-none tabular-nums" style={{ fontFamily: "var(--font-serif)" }}>
          {value.toLocaleString()}
        </span>
        {dot !== "transparent" ? (
          <span
            aria-hidden
            style={{
              width: 7, height: 7, borderRadius: 999,
              background: dot, display: "inline-block",
            }}
          />
        ) : null}
      </div>
      <div className="eyebrow mt-1">{label}</div>
    </button>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit view — v1.0 forensic event stream per docs/v1.0-audit-query-surface-spec.md.
//
// First slice ships: filter bar (5 chips), result rows with type pill +
// actor + cross-linkable refs + summary line, inline detail expand,
// chain-verification status pill in header, cursor-paginated "Load more",
// URL-serialisable filter state. Polishes deferred: density toggle,
// autocomplete on host/actor chips, multi-select on type chip,
// pretty-printed JSON detail (currently raw monospace).
// ─────────────────────────────────────────────────────────────────────────────

const ROLLOUT_EVENT_TYPES = [
  "RolloutCreated", "RolloutInstant", "RolloutAdvanced", "RolloutPaused",
  "RolloutAborted", "RolloutPromoted", "RolloutFastPromoted",
] as const;
const OVERRIDE_EVENT_TYPES = ["OverrideApplied", "OverrideCleared"] as const;
const HOST_EVENT_TYPES = ["LabelChanged", "LabelCleared", "HostDeleted"] as const;
const CONFIG_EVENT_TYPES = [
  "ConfigCreated", "ConfigRollback", "ConfigRepushed",
  "ProductDeleted", "VariantDeleted",
] as const;
const ALL_EVENT_TYPES: string[] = [
  ...ROLLOUT_EVENT_TYPES, ...OVERRIDE_EVENT_TYPES, ...HOST_EVENT_TYPES, ...CONFIG_EVENT_TYPES,
];

function AuditView({
  initialFilter,
  onHost,
  onProduct,
  onRollout,
}: {
  initialFilter?: AuditFilter;
  onHost: (uid: string) => void;
  onProduct: (product: string) => void;
  onRollout: (id: number) => void;
}) {
  // Cross-link entries (host drawer's "View all events for this host"
  // etc.) pre-fill filter chips by passing initialFilter. Default since
  // is widened to 30 days when an initialFilter narrows the result set
  // — operators arriving from a host drawer typically want history,
  // not just the last 24h.
  const [filter, setFilter] = useState<AuditFilter>(() => ({
    since: initialFilter?.since ?? (initialFilter && Object.keys(initialFilter).length > 0 ? "30d" : "24h"),
    ...(initialFilter ?? {}),
  }));
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [chain, setChain] = useState<AuditChainStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showLegacy, setShowLegacy] = useState(false);

  // Initial load + filter-change reload. We intentionally don't poll the
  // result list (spec §11.5): audit is forensic, not live; auto-refresh
  // would shuffle rows the operator is reading.
  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    api.listAudit(filter, 100)
      .then((rows) => {
        if (!alive) return;
        setEntries(rows);
        setExpanded(new Set());
        // X-Next-Cursor header isn't surfaced by api.listAudit's helper
        // (we've been using the array shape v0.2 callers expect for
        // backwards compat). For v1.0 audit pagination we re-derive
        // "has more" by checking row count — if we got back a full
        // page, there's likely more. Simplest workable cursor scheme
        // for first slice; a proper Link-header read lands in a polish
        // pass.
        setNextCursor(rows.length >= 100 ? String(rows[rows.length - 1].id) : null);
      })
      .catch((e: unknown) => {
        if (!alive) return;
        setError(e instanceof Error ? e.message : "Failed to load audit log");
      })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [filter]);

  // Chain status — load once on mount + refresh every 60s while view is open.
  useEffect(() => {
    let alive = true;
    const load = () => {
      api.verifyAuditChain()
        .then((s) => { if (alive) setChain(s); })
        .catch(() => { /* leave previous status on transient failure */ });
    };
    load();
    const t = setInterval(load, 60_000);
    return () => { alive = false; clearInterval(t); };
  }, []);

  async function loadMore() {
    if (!nextCursor) return;
    try {
      const more = await api.listAudit(filter, 100, nextCursor);
      setEntries((prev) => (prev ?? []).concat(more));
      setNextCursor(more.length >= 100 ? String(more[more.length - 1].id) : null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load more");
    }
  }

  const visible = useMemo(() => {
    if (entries === null) return null;
    if (showLegacy) return entries;
    return entries.filter((e) => Boolean(e.hash));
  }, [entries, showLegacy]);

  function setFilterPart<K extends keyof AuditFilter>(key: K, value: AuditFilter[K]) {
    setFilter((f) => ({ ...f, [key]: value }));
  }

  function clearFilters() {
    setFilter({ since: "24h" });
  }

  return (
    <div>
      <header className="mb-4 flex items-baseline justify-between gap-4 flex-wrap">
        <h2 className="text-[30px] font-light tracking-tight leading-none" style={{ fontFamily: "var(--font-serif)" }}>
          Audit
        </h2>
        <ChainStatusPill status={chain} />
      </header>

      {/* Filter bar — five chips. v1.0 first slice uses text inputs;
          autocomplete + dropdowns are polish. */}
      <section className="mb-4 card px-4 py-3">
        <div className="grid grid-cols-1 md:grid-cols-5 gap-3">
          <FilterField label="Host">
            <input
              className="field code text-xs"
              placeholder="exact instance_uid"
              value={filter.host_ref ?? ""}
              onChange={(e) => setFilterPart("host_ref", e.target.value || undefined)}
              spellCheck={false}
            />
          </FilterField>
          <FilterField label="Scope">
            <input
              className="field code text-xs"
              placeholder="contains, e.g. Shipper"
              value={filter.scope_ref ?? ""}
              onChange={(e) => setFilterPart("scope_ref", e.target.value || undefined)}
              spellCheck={false}
            />
          </FilterField>
          <FilterField label="Type">
            <select
              className="field text-xs"
              value={filter.type ?? ""}
              onChange={(e) => setFilterPart("type", e.target.value || undefined)}
            >
              <option value="">All</option>
              <optgroup label="Rollout">
                {ROLLOUT_EVENT_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
              </optgroup>
              <optgroup label="Override">
                {OVERRIDE_EVENT_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
              </optgroup>
              <optgroup label="Host">
                {HOST_EVENT_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
              </optgroup>
              <optgroup label="Config">
                {CONFIG_EVENT_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
              </optgroup>
            </select>
          </FilterField>
          <FilterField label="Actor">
            <input
              className="field text-xs"
              placeholder="contains, e.g. Praneeth"
              value={filter.actor ?? ""}
              onChange={(e) => setFilterPart("actor", e.target.value || undefined)}
              spellCheck={false}
            />
          </FilterField>
          <FilterField label="Since">
            <select
              className="field text-xs"
              value={filter.since ?? "24h"}
              onChange={(e) => setFilterPart("since", e.target.value as AuditFilter["since"])}
            >
              <option value="1h">1 hour</option>
              <option value="24h">24 hours</option>
              <option value="7d">7 days</option>
              <option value="30d">30 days</option>
              <option value="all">All time</option>
            </select>
          </FilterField>
        </div>
        <div className="mt-3 flex items-center justify-between flex-wrap gap-2">
          <label className="flex items-center gap-2 text-xs text-[var(--color-muted)] cursor-pointer">
            <input
              type="checkbox"
              checked={showLegacy}
              onChange={(e) => setShowLegacy(e.target.checked)}
            />
            Show v0.2 legacy rows
          </label>
          <button type="button" className="btn-ghost text-xs" onClick={clearFilters}>
            Clear filters
          </button>
        </div>
      </section>

      {error ? <div className="banner-error mb-4">{error}</div> : null}

      <div className="card overflow-hidden">
        {loading && entries === null ? (
          <div className="px-5 py-8 text-sm text-[var(--color-muted)]">Loading…</div>
        ) : visible === null ? (
          <div className="px-5 py-8 text-sm text-[var(--color-muted)]">No data.</div>
        ) : visible.length === 0 ? (
          <div className="px-5 py-8 text-sm text-[var(--color-muted)]">
            No events match the current filter.{" "}
            <button className="underline" onClick={clearFilters}>Clear filters</button>.
          </div>
        ) : (
          <ul className="divide-y divide-[var(--color-rule-soft)]">
            {visible.map((e) => (
              <AuditRow
                key={e.id}
                entry={e}
                expanded={expanded.has(e.id)}
                onToggle={() => setExpanded((prev) => {
                  const next = new Set(prev);
                  if (next.has(e.id)) next.delete(e.id); else next.add(e.id);
                  return next;
                })}
                onHost={onHost}
                onProduct={onProduct}
                onRollout={onRollout}
                onActor={(actor) => setFilterPart("actor", actor)}
              />
            ))}
          </ul>
        )}
      </div>

      <div className="mt-4 flex items-baseline justify-between text-xs text-[var(--color-muted)]">
        <span>
          Showing {visible?.length ?? 0}{!showLegacy && entries && entries.length !== visible?.length ? ` (${entries.length - (visible?.length ?? 0)} legacy hidden)` : ""}
        </span>
        {nextCursor ? (
          <button type="button" className="btn-ghost" onClick={loadMore}>
            Load more ▸
          </button>
        ) : null}
      </div>
    </div>
  );
}

function FilterField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="eyebrow mb-1">{label}</div>
      {children}
    </div>
  );
}

// ChainStatusPill is the always-visible header pill that shows the most
// recent /audit/verify result. Per spec §8 — green/intact for the common
// case, red on broken with a clickable affordance to scroll to the
// broken row. The "scroll to broken row" path is best-effort in this
// first slice (it surfaces the broken_at_id in the pill text; deep
// jumping to that row is polish).
function ChainStatusPill({ status }: { status: AuditChainStatus | null }) {
  if (status === null) {
    return (
      <span className="pill text-xs">
        <span className="text-[var(--color-muted)]">↻ Verifying chain…</span>
      </span>
    );
  }
  if (status.valid) {
    return (
      <span className="pill pill-ok text-xs" title={`Verified up to id #${status.valid_up_to_id}`}>
        ✓ Chain intact ({status.valid_up_to_id.toLocaleString()} events)
      </span>
    );
  }
  return (
    <span
      className="pill pill-bad text-xs"
      title="Chain integrity check failed; see broken row in the list below"
    >
      ✗ Chain broken at #{status.broken_at_id ?? 0}, valid up to #{status.valid_up_to_id}
    </span>
  );
}

// AuditRow is one entry in the result list. Two-line layout per spec §5:
// type pill + actor + cross-linkable refs + relative time + ▸ on top;
// type-derived summary on the bottom. Click to toggle inline expansion
// of the full payload.
function AuditRow({
  entry,
  expanded,
  onToggle,
  onHost,
  onProduct,
  onRollout,
  onActor,
}: {
  entry: AuditEntry;
  expanded: boolean;
  onToggle: () => void;
  onHost: (uid: string) => void;
  onProduct: (product: string) => void;
  onRollout: (id: number) => void;
  onActor: (actor: string) => void;
}) {
  const isLegacy = !entry.hash;
  const typeLabel = entry.type || (isLegacy ? "legacy" : entry.action);
  const summary = summariseAudit(entry);
  const rolloutId = extractRolloutId(entry);

  return (
    <li className="px-5 py-3">
      {/* Row is a clickable region (toggles expanded detail). Implemented
          as a div + role/keyboard handlers rather than <button> because
          the actor and scope chips inside are themselves <button>s, and
          HTML disallows nested buttons. */}
      <div
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        className="w-full text-left flex items-baseline justify-between gap-4 cursor-pointer"
        onClick={onToggle}
        onKeyDown={(ev) => {
          if (ev.key === "Enter" || ev.key === " ") {
            ev.preventDefault();
            onToggle();
          }
        }}
      >
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2 flex-wrap text-sm">
            <span
              className="pill"
              style={{ fontSize: 11, color: dotColorFor(entry) }}
            >
              {typeLabel}
            </span>
            <button
              type="button"
              className="text-[var(--color-muted)] underline-offset-2 hover:underline"
              onClick={(ev) => { ev.stopPropagation(); onActor(entry.actor); }}
            >
              {entry.actor}
            </button>
            {entry.scope_ref ? (
              <span className="code text-xs">
                <button
                  type="button"
                  className="hover:underline underline-offset-2"
                  onClick={(ev) => {
                    ev.stopPropagation();
                    if (entry.scope_kind === "instance") onHost(entry.scope_ref!);
                    else if (entry.scope_kind === "product_variant") {
                      const product = entry.scope_ref!.split("/")[0];
                      if (product) onProduct(product);
                    }
                  }}
                >
                  {entry.scope_kind === "instance" ? `instance: ${entry.scope_ref}` : entry.scope_ref}
                </button>
              </span>
            ) : entry.product ? (
              <span className="code text-xs text-[var(--color-muted)]">
                {entry.product}{entry.variant ? ` · ${entry.variant}` : ""}
              </span>
            ) : null}
            {entry.config_ref ? (
              <span className="code text-xs text-[var(--color-muted)]">cfg #{entry.config_ref}</span>
            ) : null}
          </div>
          {summary ? (
            <div className="text-xs text-[var(--color-muted)] mt-1">{summary}</div>
          ) : null}
        </div>
        <div className="flex items-baseline gap-2 shrink-0">
          <span className="text-xs text-[var(--color-muted-soft)] whitespace-nowrap">
            {sinceShort(entry.at)}
          </span>
          <span className="text-[var(--color-muted-soft)] text-xs">{expanded ? "▾" : "▸"}</span>
        </div>
      </div>

      {expanded ? (
        <div className="mt-3 pt-3 border-t border-[var(--color-rule-soft)]">
          <div className="grid grid-cols-2 md:grid-cols-3 gap-2 text-xs mb-3">
            <KV label="ID"><span className="code">#{entry.id}</span></KV>
            <KV label="At"><span className="text-[var(--color-muted)]">{new Date(entry.at).toISOString()}</span></KV>
            <KV label="Type"><span className="code">{entry.type || "(legacy)"}</span></KV>
            {entry.host_ref ? (
              <KV label="Host">
                <button
                  type="button" className="code hover:underline underline-offset-2"
                  onClick={() => onHost(entry.host_ref!)}
                >{entry.host_ref}</button>
              </KV>
            ) : null}
            {entry.scope_kind ? (
              <KV label="Scope kind"><span className="code">{entry.scope_kind}</span></KV>
            ) : null}
            {entry.config_ref ? (
              <KV label="Config"><span className="code">#{entry.config_ref}</span></KV>
            ) : null}
          </div>

          {entry.payload_json && entry.payload_json !== "{}" ? (
            <div className="mb-3">
              <div className="eyebrow mb-1">Payload</div>
              <pre className="code text-xs bg-[var(--color-paper-warm)] border border-[var(--color-rule)] rounded-md px-3 py-2 overflow-x-auto whitespace-pre-wrap" style={{ maxHeight: 240 }}>
                {prettyJSON(entry.payload_json)}
              </pre>
            </div>
          ) : entry.detail ? (
            <div className="mb-3">
              <div className="eyebrow mb-1">Detail (legacy)</div>
              <div className="text-xs text-[var(--color-muted)] whitespace-pre-wrap">{entry.detail}</div>
            </div>
          ) : null}

          {!isLegacy ? (
            <div className="mb-3">
              <div className="eyebrow mb-1">Hash chain</div>
              <div className="grid grid-cols-2 gap-2 text-xs">
                <KV label="prev_hash"><span className="code">{entry.prev_hash ? shortHash(entry.prev_hash) : "—"}</span></KV>
                <KV label="hash"><span className="code">{shortHash(entry.hash!)}</span></KV>
              </div>
            </div>
          ) : null}

          <div className="flex flex-wrap gap-2">
            {rolloutId !== null ? (
              <button
                type="button" className="btn-ghost text-xs"
                onClick={() => onRollout(rolloutId)}
              >
                View rollout #{rolloutId} ▸
              </button>
            ) : null}
            <button
              type="button" className="btn-ghost text-xs"
              onClick={() => {
                const json = JSON.stringify(entry, null, 2);
                void navigator.clipboard.writeText(json);
              }}
            >
              Copy event JSON
            </button>
          </div>
        </div>
      ) : null}
    </li>
  );
}

// summariseAudit derives the type-specific bottom-line summary per spec §5.2.
// Falls through to the legacy free-text Detail for v0.2 rows.
function summariseAudit(e: AuditEntry): string {
  if (!e.hash) return e.detail ?? "";
  let payload: Record<string, unknown> = {};
  try {
    if (e.payload_json) payload = JSON.parse(e.payload_json) as Record<string, unknown>;
  } catch { /* leave empty */ }
  switch (e.type) {
    case "RolloutCreated":
      return `${payload.rollout_kind ?? "phased"} canary_size=${payload.canary_size ?? "?"} soak_seconds=${payload.soak_seconds ?? "?"} gate_mode=${payload.gate_mode ?? "?"}`;
    case "RolloutInstant":
      return "instant rollout";
    case "RolloutAdvanced": {
      const from = payload.from_state ?? payload.event ?? "";
      const to = payload.to_state ?? "";
      if (from && to) return `${from} → ${to}`;
      if (payload.event) return `${payload.event}${payload.phase ? ` (${payload.phase})` : ""}`;
      return "advanced";
    }
    case "RolloutPaused":
      return `paused from ${payload.paused_from_state ?? "?"}`;
    case "RolloutAborted":
      return `abort_reason=${payload.abort_reason ?? "?"}${payload.applied !== undefined ? ` applied=${payload.applied}/${payload.total_canary ?? "?"}` : ""}${payload.failed !== undefined ? ` failed=${payload.failed}` : ""}`;
    case "RolloutPromoted":
      return `${payload.mode ?? "manual"} mode promote`;
    case "RolloutFastPromoted":
      return `gate_status_at_skip=${payload.gate_status_at_skip ?? "?"}`;
    case "OverrideApplied":
      return "override applied";
    case "OverrideCleared":
      return "override cleared";
    case "LabelChanged":
      return payload.product && payload.variant
        ? `re-labelled to ${payload.product}/${payload.variant}`
        : "label changed";
    case "LabelCleared":
      return "label override cleared";
    case "HostDeleted":
      return "host deleted";
    case "ConfigCreated":
      return `created ${payload.name ?? "config"}${payload.bytes ? ` (${payload.bytes} bytes)` : ""}`;
    case "ConfigRollback":
      return `rollback from #${payload.from_id ?? "?"}${payload.new_name ? ` → ${payload.new_name}` : ""}`;
    case "ConfigRepushed":
      return "operator re-pushed config to host";
    case "ProductDeleted":
      return `removed ${payload.configs_removed ?? "?"} config(s) under product ${payload.product ?? "?"}`;
    case "VariantDeleted":
      return `removed ${payload.configs_removed ?? "?"} config(s) under ${payload.product ?? "?"}/${payload.variant ?? "?"}`;
    default:
      return e.detail ?? "";
  }
}

// extractRolloutId pulls a rollout_id out of the structured payload if
// the event is rollout-shaped. Surfaces the "View rollout #N" affordance
// in the detail panel.
function extractRolloutId(e: AuditEntry): number | null {
  if (!e.payload_json) return null;
  try {
    const payload = JSON.parse(e.payload_json) as Record<string, unknown>;
    const id = payload.rollout_id;
    if (typeof id === "number" && Number.isFinite(id)) return id;
  } catch { /* not JSON or no field */ }
  return null;
}

// dotColorFor returns the colour used for the type pill, classifying
// events by family. Aborted/HostDeleted lean red; OverrideApplied/Cleared
// lean amber; everything else is neutral.
function dotColorFor(e: AuditEntry): string {
  switch (e.type) {
    case "RolloutAborted":
    case "HostDeleted":
      return "var(--color-danger)";
    case "OverrideApplied":
    case "OverrideCleared":
    case "LabelChanged":
    case "RolloutPaused":
      return "#7a5e22";
    case "RolloutAdvanced":
    case "RolloutPromoted":
    case "RolloutFastPromoted":
      return "var(--color-accent-ink)";
    default:
      return "inherit";
  }
}

// prettyJSON pretty-prints a JSON string. If parsing fails, returns the
// raw string — operators investigating an event whose payload is malformed
// shouldn't be left with nothing.
function prettyJSON(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// PlaceholderView is the hold-pane for sidebar entries (Rollouts, Audit)
// whose UI hasn't shipped yet. Shows the v1.0 IA shape — operators see
// the entries in the sidebar — without pretending the panel works.
function PlaceholderView({ title, description, hint }: { title: string; description: string; hint: string }) {
  return (
    <div>
      <header className="mb-8">
        <h2 className="text-[30px] font-light tracking-tight leading-none" style={{ fontFamily: "var(--font-serif)" }}>
          {title}
        </h2>
        <p className="mt-2 text-sm text-[var(--color-muted)]">{description}</p>
      </header>
      <div className="card px-6 py-8 text-sm text-[var(--color-muted)]">
        <strong className="block mb-1 text-[var(--color-ink)]">Coming soon</strong>
        {hint}
      </div>
    </div>
  );
}

function Th({ children, className = "" }: { children: React.ReactNode; className?: string }) {
  return <th className={`px-5 py-2.5 eyebrow font-medium ${className}`} style={{ fontSize: 10.5 }}>{children}</th>;
}

// ─────────────────────────────────────────────────────────────────────────────
// Rollouts UI section — list view + drawer with phase pills + live polling +
// operator action buttons. Implements spec §10 + §11 of the publish dialog
// spec (the live progress view) and the standalone Rollouts section
// referenced from the Fleet view's in-flight strip.
//
// Per-host failure detail tabs (Failed / Pending / Applied lists) deferred
// to a follow-up — this slice surfaces aggregate counts only.
// ─────────────────────────────────────────────────────────────────────────────

const ROLLOUT_PHASES: Rollout["state"][] = ["validating", "canary", "soak", "promoting", "done"];

// phaseTone maps a rollout state to a tone for the phase pill colour.
function phaseTone(state: Rollout["state"]): "ok" | "warn" | "bad" | "neutral" {
  if (state === "done") return "ok";
  if (state === "aborted" || state === "paused") return "warn";
  return "neutral";
}

// rolloutSummary builds a one-line phase-progress string for a rollout
// card on the Fleet strip and the Rollouts list. Exposed copy that
// matches spec §5 (cards) + §10 (per-phase progress lines).
function rolloutSummary(r: Rollout, agg?: ApplyAggregate): string {
  switch (r.state) {
    case "validating":
      return "Validating…";
    case "canary":
      if (agg) return `Canary ${agg.applied}/${agg.total_canary} applied`;
      return "Canary in flight";
    case "soak":
      return r.gate_passed_at ? "Soak (gate passed; awaiting elapse)" : "Soak (gate evaluating)";
    case "promoting":
      if (agg) return `Promote ${agg.applied}/${agg.total_canary + agg.total_promote} applied`;
      return "Promoting";
    case "paused":
      return `Paused (from ${r.prev_state ?? "unknown"})`;
    case "done":
      return "Done";
    case "aborted":
      return `Aborted${r.abort_reason ? `: ${r.abort_reason}` : ""}`;
  }
}

// HostInFlightRolloutCard is the per-rollout card shown in the host
// drawer's In-flight rollouts strip (spec §6, host-drawer zone 4). It's
// host-aware in a way the Fleet's RolloutCard isn't: it shows *this
// host's* role (canary vs promote subset) and *this host's* apply state
// for the rollout, instead of fleet-wide aggregates. The "View rollout"
// button hands off to the Rollouts drawer for full ApplyState detail.
// STUCK_APPLY_MS is the threshold past which an apply_state row in
// "applying"/"pending" is considered stuck — the server pushed the
// config but the agent hasn't acked. Operators see this in the UI as
// an amber "stuck" treatment and can hit Re-push to force-resend.
// 2 minutes balances normal apply latency (a few seconds typical)
// against avoiding false alarms during agent restarts.
const STUCK_APPLY_MS = 2 * 60 * 1000;

function HostInFlightRolloutCard({
  rollout,
  applyState,
  hostUID,
  onOpen,
  onRepushed,
}: {
  rollout: Rollout;
  applyState: ApplyState;
  hostUID: string;
  onOpen: () => void;
  onRepushed: () => void;
}) {
  const [repushing, setRepushing] = useState(false);
  const [repushMsg, setRepushMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  const tone = phaseTone(rollout.state);
  const dot =
    tone === "ok"   ? "var(--color-accent)" :
    tone === "warn" ? "#caa247" :
    tone === "bad"  ? "var(--color-danger)" :
    "var(--color-muted-soft)";
  const role = applyState.is_canary ? "canary subset" : "promote subset";

  // "Stuck" = pending/applying for longer than STUCK_APPLY_MS. Computed
  // client-side from updated_at so we don't need a new backend signal.
  const updatedAtMs = applyState.updated_at ? Date.parse(applyState.updated_at) : 0;
  const ageMs = updatedAtMs ? Date.now() - updatedAtMs : 0;
  const isStuck = (applyState.state === "applying" || applyState.state === "pending")
    && ageMs > STUCK_APPLY_MS;

  const stateLine = (() => {
    const stamp = applyState.updated_at ? sinceShort(applyState.updated_at) : "—";
    switch (applyState.state) {
      case "pending":
        return isStuck ? `pending — stuck since ${stamp}` : "pending";
      case "applying":
        return isStuck ? `applying — stuck since ${stamp}` : `applying (${stamp})`;
      case "applied":
        return `applied (${stamp})`;
      case "failed":
        return applyState.last_error
          ? `failed apply ${stamp} — ${applyState.last_error}`
          : `failed apply ${stamp}`;
    }
  })();

  async function doRepush() {
    setRepushing(true);
    setRepushMsg(null);
    try {
      await api.repushAgent(hostUID);
      setRepushMsg({ kind: "ok", text: "Re-push sent. Watch for an applied ack." });
      onRepushed();
      setTimeout(() => setRepushMsg(null), 3000);
    } catch (err) {
      setRepushMsg({ kind: "err", text: err instanceof Error ? err.message : "Re-push failed" });
    } finally {
      setRepushing(false);
    }
  }

  return (
    <div
      className="card px-4 py-3"
      style={isStuck ? { borderColor: "#caa247", background: "rgba(202, 162, 71, 0.06)" } : undefined}
    >
      <div className="flex items-baseline justify-between gap-2 flex-wrap">
        <div className="text-sm font-medium">
          <span className="text-[var(--color-muted)]">#{rollout.id}</span>
          <span className="text-[var(--color-muted-soft)]"> · </span>
          <span>{rollout.scope_ref}</span>
        </div>
        <span className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
          <span style={{ width: 6, height: 6, borderRadius: 999, background: dot, display: "inline-block" }} />
          {rollout.state}
        </span>
      </div>
      <div className="text-xs text-[var(--color-muted)] mt-1">
        This host is in the {role}
        <span className="mx-1">·</span>
        <span style={
          applyState.state === "failed" ? { color: "var(--color-danger)" } :
          isStuck ? { color: "#7a5e22" } :
          undefined
        }>
          {stateLine}
        </span>
      </div>
      {isStuck ? (
        <p className="hint" style={{ marginTop: 6, color: "#7a5e22" }}>
          The server pushed the config but the agent hasn&apos;t acked in over {Math.floor(STUCK_APPLY_MS / 60_000)}m.
          The agent may not have dispatched the push — Re-push forces magpied to send it again.
        </p>
      ) : null}
      {repushMsg ? (
        <p
          className="text-xs mt-2"
          style={{ color: repushMsg.kind === "err" ? "var(--color-danger)" : "var(--color-accent-ink)" }}
        >
          {repushMsg.text}
        </p>
      ) : null}
      <div className="mt-2 flex justify-end gap-2">
        {(isStuck || applyState.state === "failed") ? (
          <button
            type="button"
            className="btn-ghost text-xs"
            onClick={doRepush}
            disabled={repushing}
          >
            {repushing ? "Re-pushing…" : "Re-push config"}
          </button>
        ) : null}
        <button type="button" className="btn-ghost text-xs" onClick={onOpen}>
          View rollout ▸
        </button>
      </div>
    </div>
  );
}

function RolloutCard({ r, onClick }: { r: Rollout; onClick: () => void }) {
  const tone = phaseTone(r.state);
  const dot =
    tone === "ok"   ? "var(--color-accent)" :
    tone === "warn" ? "#caa247" :
    tone === "bad"  ? "var(--color-danger)" :
    "var(--color-muted-soft)";
  return (
    <button
      type="button"
      onClick={onClick}
      className="card text-left px-4 py-3 shrink-0 hover:bg-[var(--color-paper-warm)]/40 transition-colors"
      style={{ minWidth: 280 }}
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="font-medium text-sm">{r.scope_ref}</span>
        <span className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
          <span style={{ width: 6, height: 6, borderRadius: 999, background: dot, display: "inline-block" }} />
          {r.state}
        </span>
      </div>
      <div className="text-xs text-[var(--color-muted)] mt-1">{rolloutSummary(r)}</div>
      <div className="text-xs text-[var(--color-muted-soft)] mt-1">
        #{r.id} · {r.created_by} · {sinceShort(r.created_at)}
      </div>
    </button>
  );
}

function RolloutsView({
  rollouts,
  onRollout,
}: {
  rollouts: Rollout[] | null;
  onRollout: (r: Rollout) => void;
}) {
  if (rollouts === null) {
    return (
      <div className="pt-32 text-center text-[var(--color-muted)]">Connecting…</div>
    );
  }
  const inFlight = rollouts.filter((r) => r.state !== "done" && r.state !== "aborted");
  const recent = rollouts.filter((r) => r.state === "done" || r.state === "aborted").slice(0, 50);
  return (
    <div>
      <header className="mb-6">
        <h2 className="text-[30px] font-light tracking-tight leading-none" style={{ fontFamily: "var(--font-serif)" }}>
          Rollouts
        </h2>
        <p className="mt-2 text-sm text-[var(--color-muted)]">
          {inFlight.length} in flight · {recent.length} recent
        </p>
      </header>

      {inFlight.length > 0 ? (
        <section className="mb-8">
          <span className="eyebrow mb-2 block">In flight</span>
          <ul className="card divide-y divide-[var(--color-rule-soft)]">
            {inFlight.map((r) => (
              <RolloutListRow key={r.id} r={r} onClick={() => onRollout(r)} />
            ))}
          </ul>
        </section>
      ) : null}

      <section>
        <span className="eyebrow mb-2 block">Recent</span>
        {recent.length === 0 ? (
          <div className="card px-5 py-6 text-sm text-[var(--color-muted)]">
            No completed rollouts yet.
          </div>
        ) : (
          <ul className="card divide-y divide-[var(--color-rule-soft)]">
            {recent.map((r) => (
              <RolloutListRow key={r.id} r={r} onClick={() => onRollout(r)} />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function RolloutListRow({ r, onClick }: { r: Rollout; onClick: () => void }) {
  const tone = phaseTone(r.state);
  const pillCls =
    tone === "ok"   ? "pill pill-ok" :
    tone === "warn" ? "pill" :
    "pill";
  return (
    <li
      className="px-5 py-3 flex items-baseline justify-between gap-4 cursor-pointer hover:bg-[var(--color-paper-warm)]/50"
      onClick={onClick}
    >
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium truncate">
          <span className="code text-xs text-[var(--color-muted)] mr-2">#{r.id}</span>
          {r.scope_ref}
          <span className="text-[var(--color-muted)] text-xs ml-2">{r.rollout_kind}</span>
        </div>
        <div className="text-xs text-[var(--color-muted)] mt-0.5">{rolloutSummary(r)}</div>
      </div>
      <div className="flex items-baseline gap-3 shrink-0">
        <span className={pillCls}>{r.state}</span>
        <span className="text-xs text-[var(--color-muted-soft)] whitespace-nowrap">
          {r.created_by} · {sinceShort(r.created_at)}
        </span>
      </div>
    </li>
  );
}

// RolloutDrawer is the live-progress view — phase pills, apply state
// aggregates, per-host failure detail tabs, operator action buttons.
// Polls the backend every 2.5s for live updates while open. Closes
// when the operator hits Close or the rollout reaches a terminal state
// and the operator dismisses.
function RolloutDrawer({
  rolloutId,
  agents,
  onClose,
  onChanged,
  onHost,
}: {
  rolloutId: number;
  agents: Agent[];
  onClose: () => void;
  onChanged: () => void;
  onHost: (uid: string) => void;
}) {
  const [data, setData] = useState<RolloutWithAggregate | null>(null);
  const [applyState, setApplyState] = useState<ApplyState[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [actionPending, setActionPending] = useState<string | null>(null);
  // Tab default per spec §10: Failed when there's any, otherwise Applied.
  // Once the operator picks a tab manually we honour it (don't keep
  // flipping back to Failed every time the failure count changes).
  const [tab, setTab] = useState<"failed" | "pending" | "applied" | null>(null);

  useEffect(() => {
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = prev; };
  }, []);

  const load = useCallback(async () => {
    try {
      const [d, rows] = await Promise.all([
        api.getRollout(rolloutId),
        api.listApplyState(rolloutId, 200),
      ]);
      setData(d);
      setApplyState(rows);
    } catch (err) {
      // Transient — leave whatever we last rendered. Auth issues bubble
      // through the global AuthRequiredError → sign-in modal.
      if (!(err instanceof AuthRequiredError)) {
        // Optional: surface a banner on persistent failure. For now, silent.
      }
    }
  }, [rolloutId]);

  useEffect(() => {
    load();
    const t = setInterval(load, 2500);
    return () => clearInterval(t);
  }, [load]);

  async function doAction(name: string, fn: () => Promise<unknown>) {
    setActionPending(name);
    setError(null);
    try {
      await fn();
      await load();
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : `${name} failed`);
    } finally {
      setActionPending(null);
    }
  }

  const r = data?.rollout;
  const agg = data?.apply_state_summary;
  const isTerminal = r ? r.state === "done" || r.state === "aborted" : false;
  const canPause = r ? r.state === "soak" || r.state === "promoting" : false;
  const canResume = r?.state === "paused";
  const canAbort = r ? !isTerminal : false;
  const canFastPromote = r
    ? r.state === "soak" || (r.state === "paused" && r.prev_state === "soak")
    : false;
  const canPromote = r
    ? r.state === "soak" && r.gate_mode === "manual" && Boolean(r.gate_passed_at)
    : false;

  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      <aside role="dialog" aria-modal="true" className="drawer">
        <header className="px-7 py-5 border-b border-[var(--color-rule)] flex items-baseline justify-between gap-4">
          <div className="min-w-0">
            <div className="eyebrow">Rollout</div>
            <h2 className="mt-1 text-[22px] font-light tracking-tight truncate" style={{ fontFamily: "var(--font-serif)" }}>
              {r ? <>#{r.id} <span className="text-[var(--color-muted-soft)]">· </span>{r.scope_ref}</> : `#${rolloutId}`}
            </h2>
            {r ? (
              <div className="text-xs text-[var(--color-muted)] mt-1">
                {r.rollout_kind} · gate {r.gate_mode} · started {sinceShort(r.created_at)} by {r.created_by}
              </div>
            ) : null}
          </div>
          <button type="button" className="btn-ghost" onClick={onClose}>Close</button>
        </header>

        <div className="flex-1 min-h-0 overflow-y-auto px-7 py-5">
          {error ? <div className="banner-error mb-4">{error}</div> : null}

          {/* Phase indicator — pills laid out left to right showing the
              full Phased pipeline. State colours: done (green dot),
              active (animated dot), pending (outlined). For Instant
              rollouts, Validate→Promoting→Done; for Phased the full
              five-pill sequence. */}
          <section className="mb-6">
            <div className="eyebrow mb-2">Phase</div>
            {r ? (
              <PhaseIndicator state={r.state} kind={r.rollout_kind} />
            ) : (
              <div className="text-sm text-[var(--color-muted)]">Loading…</div>
            )}
            {r ? (
              <p className="hint" style={{ marginTop: 12 }}>{rolloutSummary(r, agg)}</p>
            ) : null}
          </section>

          {/* Apply state aggregates — operator at-a-glance progress. */}
          {agg ? (
            <section className="mb-6">
              <div className="eyebrow mb-2">Apply state</div>
              <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-sm">
                <KV label="applied">
                  <span className="font-medium text-[var(--color-accent-ink)]">{agg.applied}</span>
                </KV>
                <KV label="applying">
                  <span className="font-medium">{agg.applying}</span>
                </KV>
                <KV label="pending">
                  <span className="font-medium">{agg.pending}</span>
                </KV>
                <KV label="failed">
                  <span className="font-medium text-[var(--color-danger)]">{agg.failed}</span>
                </KV>
              </div>
              <div className="mt-3 text-xs text-[var(--color-muted)]">
                Canary subset: {agg.total_canary} · Promote subset: {agg.total_promote}
              </div>
            </section>
          ) : null}

          {/* Per-host detail tabs — spec §10 Failed/Pending/Applied. Failed
              gets pride of place by default-selecting it whenever any
              failures exist; once the operator picks another tab manually
              we honour it and stop flipping. Cross-link from rows to the
              host drawer so the operator can drill from "this host failed"
              into "what's wrong with this host." */}
          {applyState.length > 0 ? (
            <ApplyStateTabs
              rows={applyState}
              agents={agents}
              tab={tab}
              setTab={setTab}
              onHost={onHost}
              onRepushed={() => { void load(); onChanged(); }}
            />
          ) : null}

          {/* Operator action buttons — Pause / Resume / Abort / Fast-Promote
              / Promote. Each button only enables when its action is legal
              from the current state (mirrors the server-side state-predicate
              gate; trying anyway gets a 409 surfaced inline above). */}
          <section className="mb-6">
            <div className="eyebrow mb-2">Operator actions</div>
            <div className="flex flex-wrap gap-2">
              <button
                type="button" className="btn-ghost"
                disabled={!canPause || actionPending !== null}
                onClick={() => doAction("pause", () => api.pauseRollout(rolloutId))}
              >
                {actionPending === "pause" ? "Pausing…" : "Pause"}
              </button>
              <button
                type="button" className="btn-ghost"
                disabled={!canResume || actionPending !== null}
                onClick={() => doAction("resume", () => api.resumeRollout(rolloutId))}
              >
                {actionPending === "resume" ? "Resuming…" : "Resume"}
              </button>
              <button
                type="button" className="btn-danger"
                disabled={!canAbort || actionPending !== null}
                onClick={() => {
                  const reason = window.prompt("Optional abort reason:");
                  if (reason === null) return; // user cancelled
                  void doAction("abort", () => api.abortRollout(rolloutId, reason || undefined));
                }}
              >
                {actionPending === "abort" ? "Aborting…" : "Abort"}
              </button>
              <button
                type="button" className="btn-ghost"
                disabled={!canFastPromote || actionPending !== null}
                onClick={() => doAction("fast-promote", () => api.fastPromoteRollout(rolloutId))}
              >
                {actionPending === "fast-promote" ? "Promoting…" : "Fast-promote ▸"}
              </button>
              {r?.gate_mode === "manual" ? (
                <button
                  type="button" className="btn"
                  disabled={!canPromote || actionPending !== null}
                  onClick={() => doAction("promote", () => api.promoteRollout(rolloutId))}
                >
                  {actionPending === "promote" ? "Promoting…" : "Promote ▸"}
                </button>
              ) : null}
            </div>
            {r?.gate_mode === "manual" && r.state === "soak" && !r.gate_passed_at ? (
              <p className="hint" style={{ marginTop: 6 }}>
                Promote unlocks once the gate passes — soak window must elapse with healthy canary.
              </p>
            ) : null}
          </section>

          {/* Compact metadata — covers what spec §10 calls "operator can
              read along" without dominating the surface. */}
          {r ? (
            <section>
              <div className="eyebrow mb-2">Metadata</div>
              <div className="grid grid-cols-2 gap-3 text-sm">
                <KV label="config_id">
                  <span className="code text-xs">{r.config_id}</span>
                </KV>
                <KV label="prior_config_id">
                  <span className="code text-xs">{r.prior_config_id ?? "—"}</span>
                </KV>
                <KV label="canary_size">
                  <span className="code text-xs">{r.canary_size ?? "—"}</span>
                </KV>
                <KV label="soak_seconds">
                  <span className="code text-xs">{r.soak_seconds}</span>
                </KV>
                {r.gate_passed_at ? (
                  <KV label="gate_passed_at">
                    <span className="text-xs text-[var(--color-muted)]">{sinceShort(r.gate_passed_at)}</span>
                  </KV>
                ) : null}
                {r.aborted_at ? (
                  <KV label="aborted_at">
                    <span className="text-xs text-[var(--color-danger)]">{sinceShort(r.aborted_at)}</span>
                  </KV>
                ) : null}
                {r.done_at ? (
                  <KV label="done_at">
                    <span className="text-xs text-[var(--color-accent-ink)]">{sinceShort(r.done_at)}</span>
                  </KV>
                ) : null}
              </div>
            </section>
          ) : null}
        </div>
      </aside>
    </>
  );
}

// PhaseIndicator renders the rollout's phase progress as five (Phased)
// or three (Instant) connected pills. Each pill is one of three states:
// done (filled green), active (filled accent), pending (outlined).
// ApplyStateTabs renders the per-host failure detail tabs (Failed /
// Pending / Applied) per spec §10. Each tab lists rows with host name
// (cross-linked to host drawer when the agent is in the loaded fleet),
// state pill, last-error preview for failed rows, and last-update time.
//
// Default tab: Failed when any failures exist; Applied otherwise. Once
// the operator clicks a tab the choice is sticky for the drawer's
// lifetime (parent's `tab` state).
function ApplyStateTabs({
  rows,
  agents,
  tab,
  setTab,
  onHost,
  onRepushed,
}: {
  rows: ApplyState[];
  agents: Agent[];
  tab: "failed" | "pending" | "applied" | null;
  setTab: (t: "failed" | "pending" | "applied" | null) => void;
  onHost: (uid: string) => void;
  onRepushed: () => void;
}) {
  const failed = useMemo(() => rows.filter((r) => r.state === "failed"), [rows]);
  const pending = useMemo(() => rows.filter((r) => r.state === "pending" || r.state === "applying"), [rows]);
  const applied = useMemo(() => rows.filter((r) => r.state === "applied"), [rows]);

  const effective: "failed" | "pending" | "applied" = tab ?? (failed.length > 0 ? "failed" : "applied");
  const visible = effective === "failed" ? failed : effective === "pending" ? pending : applied;

  // Index for cross-link: instance_uid → host name (when known). Falls
  // back to a uid prefix when the agent isn't currently in the loaded
  // fleet (deleted host, host that hasn't heartbeat'd in a while).
  const hostNameByUid = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of agents) {
      const name = a.attributes?.["host.name"];
      if (name) m.set(a.instance_uid, name);
    }
    return m;
  }, [agents]);

  // Hosts in pending/applying for >2 min are flagged as stuck — operator
  // can hit Re-push per row to force-resend the resolved config. Same
  // threshold as the host drawer's in-flight strip.
  const stuck = useMemo(
    () => pending.filter((r) => {
      const t = r.updated_at ? Date.parse(r.updated_at) : 0;
      return t > 0 && (Date.now() - t) > STUCK_APPLY_MS;
    }),
    // recompute when rows reference changes; Date.now() update doesn't
    // need to trigger memo invalidation — the drawer's poll handles that
    [pending],
  );

  return (
    <section className="mb-6">
      <div className="eyebrow mb-2">Per-host status</div>
      {stuck.length > 0 ? (
        <div className="banner-notice mb-2">
          <strong>{stuck.length} host{stuck.length > 1 ? "s" : ""} stuck applying.</strong>{" "}
          The server pushed the config but the agent hasn&apos;t acked in over{" "}
          {Math.floor(STUCK_APPLY_MS / 60_000)}m. Use the Re-push button next to each row to force
          a re-send. If the next ack still doesn&apos;t arrive, check the agent&apos;s service health.
        </div>
      ) : null}
      <div className="seg" role="tablist">
        <button
          type="button" role="tab"
          aria-pressed={effective === "failed"}
          onClick={() => setTab("failed")}
        >
          Failed ({failed.length})
        </button>
        <button
          type="button" role="tab"
          aria-pressed={effective === "pending"}
          onClick={() => setTab("pending")}
        >
          Pending ({pending.length})
        </button>
        <button
          type="button" role="tab"
          aria-pressed={effective === "applied"}
          onClick={() => setTab("applied")}
        >
          Applied ({applied.length})
        </button>
      </div>

      <div className="card mt-2 overflow-hidden">
        {visible.length === 0 ? (
          <div className="px-5 py-6 text-sm text-[var(--color-muted)]">
            {effective === "failed" ? "No failures." :
             effective === "pending" ? "No hosts pending." :
             "No hosts in this state."}
          </div>
        ) : (
          <ul className="divide-y divide-[var(--color-rule-soft)] max-h-[320px] overflow-y-auto">
            {visible.slice(0, 100).map((r) => {
              const host = hostNameByUid.get(r.instance_uid) ?? r.instance_uid.slice(0, 16);
              const known = hostNameByUid.has(r.instance_uid);
              const t = r.updated_at ? Date.parse(r.updated_at) : 0;
              const isStuck = (r.state === "pending" || r.state === "applying")
                && t > 0 && (Date.now() - t) > STUCK_APPLY_MS;
              const canRepush = isStuck || r.state === "failed";
              return (
                <li
                  key={r.instance_uid}
                  className="px-5 py-2 text-sm"
                  style={isStuck ? { background: "rgba(202, 162, 71, 0.06)" } : undefined}
                >
                  <div className="flex items-baseline justify-between gap-3">
                    <button
                      type="button"
                      className={known ? "font-medium underline-offset-2 hover:underline" : "font-medium"}
                      onClick={() => { if (known) onHost(r.instance_uid); }}
                      disabled={!known}
                      title={known ? "Open host drawer" : "Host not currently loaded"}
                    >
                      {host}
                    </button>
                    <div className="flex items-baseline gap-2 shrink-0">
                      {r.is_canary ? (
                        <span className="text-xs text-[var(--color-muted-soft)]">canary</span>
                      ) : null}
                      {isStuck ? (
                        <span className="text-xs" style={{ color: "#7a5e22" }}>stuck</span>
                      ) : null}
                      <span className="text-xs text-[var(--color-muted-soft)] whitespace-nowrap">
                        {sinceShort(r.updated_at)}
                      </span>
                      {canRepush ? (
                        <RepushButton uid={r.instance_uid} onDone={onRepushed} />
                      ) : null}
                    </div>
                  </div>
                  {r.last_error && r.state === "failed" ? (
                    <div className="text-xs code text-[var(--color-danger)] mt-1" style={{ whiteSpace: "pre-wrap" }}>
                      {r.last_error}
                    </div>
                  ) : null}
                </li>
              );
            })}
            {visible.length > 100 ? (
              <li className="px-5 py-2 text-xs text-[var(--color-muted)]">
                Showing first 100 of {visible.length}. Use the Audit view filtered by rollout to see all.
              </li>
            ) : null}
          </ul>
        )}
      </div>
    </section>
  );
}

// RepushButton is a compact "Re-push" affordance for a single host. Used
// inline in the rollout drawer's per-host status list — operator hits it
// to force-resend the resolved config to a stuck/failed agent.
function RepushButton({ uid, onDone }: { uid: string; onDone: () => void }) {
  const [pending, setPending] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function go(ev: React.MouseEvent) {
    ev.stopPropagation();
    setPending(true);
    setErr(null);
    try {
      await api.repushAgent(uid);
      setDone(true);
      onDone();
      setTimeout(() => setDone(false), 2500);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
      setTimeout(() => setErr(null), 4000);
    } finally {
      setPending(false);
    }
  }
  if (err) {
    return <span className="text-xs" style={{ color: "var(--color-danger)" }}>{err}</span>;
  }
  if (done) {
    return <span className="text-xs" style={{ color: "var(--color-accent-ink)" }}>sent ✓</span>;
  }
  return (
    <button type="button" className="btn-ghost text-xs" onClick={go} disabled={pending}>
      {pending ? "…" : "Re-push"}
    </button>
  );
}

function PhaseIndicator({ state, kind }: { state: Rollout["state"]; kind: Rollout["rollout_kind"] }) {
  const phases: Rollout["state"][] =
    kind === "instant" ? ["validating", "promoting", "done"] : ROLLOUT_PHASES;

  // For aborted state, render the full sequence with a red "Aborted"
  // pill replacing the active one.
  if (state === "aborted") {
    return (
      <div className="flex items-center gap-2 flex-wrap">
        <span className="pill pill-bad">Aborted</span>
      </div>
    );
  }
  if (state === "paused") {
    return (
      <div className="flex items-center gap-2 flex-wrap">
        <span className="pill" style={{ borderColor: "#caa247", color: "#7a5e22", background: "#fff5e0" }}>
          Paused
        </span>
        <span className="text-xs text-[var(--color-muted)]">
          (will resume from prior state)
        </span>
      </div>
    );
  }

  const activeIdx = phases.indexOf(state);
  return (
    <div className="flex items-center gap-2 flex-wrap">
      {phases.map((p, i) => {
        const isDone = activeIdx > i;
        const isActive = activeIdx === i;
        let cls = "pill";
        let style: React.CSSProperties | undefined;
        if (isDone) cls = "pill pill-ok";
        else if (isActive) {
          cls = "pill";
          style = {
            color: "var(--color-accent-ink)",
            borderColor: "var(--color-accent)",
            background: "var(--color-paper-warm)",
            fontWeight: 500,
          };
        } else {
          style = { opacity: 0.5 };
        }
        return (
          <span key={p} className={cls} style={style}>
            {isActive ? "▸ " : isDone ? "✓ " : ""}
            {p}
          </span>
        );
      })}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Editor drawer (unified YAML + history + rollback)
// ─────────────────────────────────────────────────────────────────────────────

function EditorDrawer({
  product,
  variant,
  revisions,
  onClose,
  onChanged,
}: {
  product: string;
  variant: string;
  revisions: Config[];
  onClose: () => void;
  onChanged: () => void;
}) {
  // If variant is the sentinel "__new__" (chosen by "+ New variant"), let the
  // user pick which variant to create. Otherwise it's locked.
  const isNew = variant === "__new__";
  const [pickedVariant, setPickedVariant] = useState<VariantKey>(
    isNew ? "linux" : (variant as VariantKey) === "windows" || (variant as VariantKey) === "linux" || (variant as VariantKey) === "kubernetes" ? (variant as VariantKey) : "custom"
  );
  const activeVariant = isNew ? pickedVariant : variant;

  const [tab, setTab] = useState<"edit" | "history">("edit");
  const active = revisions[0];

  const [yaml, setYaml] = useState<string>(active?.yaml ?? templateFor(pickedVariant, product));
  const [yamlDirty, setYamlDirty] = useState<boolean>(Boolean(active));
  const [name, setName] = useState<string>(active ? `${product}-${activeVariant}-v${revisions.length + 1}` : `${product}-${activeVariant}-v1`);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);

  // v1.0 publish dialog modal opens on top of this drawer when the
  // operator hits Publish. The dialog handles blast-radius display +
  // rollout configuration (Phased/Instant, canary, soak, gate); on
  // success it calls onPublished which closes both the dialog and
  // this drawer.
  const [publishOpen, setPublishOpen] = useState(false);

  useEffect(() => {
    // Lock scroll while drawer is open
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = prev; };
  }, []);

  function pickVariantKey(k: VariantKey) {
    setPickedVariant(k);
    if (!yamlDirty) setYaml(templateFor(k, product));
    setName(`${product}-${k}-v1`);
  }

  async function rollback(id: number) {
    setPending(true);
    setError(null);
    try {
      await api.rollback(id);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed");
    } finally {
      setPending(false);
    }
  }

  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      <aside role="dialog" aria-modal="true" className="drawer">
        <header className="px-7 py-5 border-b border-[var(--color-rule)] flex items-baseline justify-between gap-4">
          <div>
            <div className="eyebrow">Edit configuration</div>
            <h2 className="mt-1 text-[22px] font-light tracking-tight" style={{ fontFamily: "var(--font-serif)" }}>
              <span>{product}</span>
              <span className="text-[var(--color-muted-soft)]"> · </span>
              <span>{activeVariant}</span>
            </h2>
          </div>
          <button type="button" className="btn-ghost" onClick={onClose}>Close</button>
        </header>

        <div className="px-7 pt-4">
          <div className="seg" role="tablist">
            <button type="button" role="tab" aria-pressed={tab === "edit"} onClick={() => setTab("edit")}>Edit</button>
            <button type="button" role="tab" aria-pressed={tab === "history"} onClick={() => setTab("history")}>
              History {revisions.length > 0 ? `(${revisions.length})` : ""}
            </button>
          </div>
        </div>

        {tab === "edit" ? (
          <div className="flex-1 min-h-0 flex flex-col px-7 pt-4 pb-6">
            {error ? <div className="banner-error mb-4">{error}</div> : null}

            {isNew ? (
              <div className="mb-4">
                <div className="eyebrow mb-2">Pick a variant</div>
                <div className="seg flex-wrap">
                  {VARIANTS.map((v) => (
                    <button
                      key={v.key}
                      type="button"
                      aria-pressed={pickedVariant === v.key}
                      onClick={() => pickVariantKey(v.key)}
                      title={v.sub}
                    >
                      {v.label}
                    </button>
                  ))}
                </div>
              </div>
            ) : null}

            <label className="block mb-3">
              <span className="eyebrow">Revision name</span>
              <input
                className="field mt-1.5"
                value={name}
                onChange={(e) => setName(e.target.value)}
                spellCheck={false}
              />
            </label>

            <div className="flex-1 min-h-0 flex flex-col">
              <div className="flex items-baseline justify-between">
                <span className="eyebrow">Pipeline YAML</span>
                {yamlDirty ? (
                  <button
                    type="button"
                    className="btn-ghost"
                    onClick={() => {
                      setYaml(active?.yaml ?? templateFor(pickedVariant, product));
                      setYamlDirty(false);
                    }}
                  >
                    Reset
                  </button>
                ) : null}
              </div>
              <textarea
                className="field code mt-1.5 flex-1 min-h-[300px] resize-none leading-relaxed"
                value={yaml}
                onChange={(e) => { setYaml(e.target.value); setYamlDirty(true); }}
                spellCheck={false}
                style={{ whiteSpace: "pre" }}
              />
              <p className="hint">
                Replace <span className="kbd">your-otlp-backend.example.com</span> and <span className="kbd">&lt;INGESTION-TOKEN&gt;</span> with your backend. Validated server-side before broadcast.
              </p>
            </div>

            <div className="mt-5 pt-4 border-t border-[var(--color-rule-soft)] flex items-center justify-between gap-4">
              <span className="text-xs text-[var(--color-muted)]">
                {ok ? (
                  <span className="text-[var(--color-accent-ink)]">Published · rolling to matching agents.</span>
                ) : (
                  <>Will start a phased rollout to agents with <span className="kbd">product={product}</span> <span className="kbd">variant={activeVariant}</span>.</>
                )}
              </span>
              <button
                type="button"
                className="btn"
                disabled={pending || !name.trim() || !yaml.trim()}
                onClick={() => setPublishOpen(true)}
              >
                Publish ▸
              </button>
            </div>
          </div>
        ) : (
          <div className="flex-1 min-h-0 overflow-y-auto px-7 pt-4 pb-6">
            {revisions.length === 0 ? (
              <div className="text-sm text-[var(--color-muted)]">No revisions yet.</div>
            ) : (
              <ul className="divide-y divide-[var(--color-rule-soft)]">
                {revisions.map((c, i) => (
                  <li key={c.id} className="py-3 flex items-baseline justify-between gap-4">
                    <div className="min-w-0">
                      <div className="text-sm truncate">
                        <span className="code text-xs text-[var(--color-muted)] mr-2">#{c.id}</span>
                        {c.name}
                        {i === 0 ? <span className="pill pill-ok ml-2" style={{ fontSize: 10 }}>active</span> : null}
                      </div>
                      <div className="text-xs text-[var(--color-muted)] mt-0.5">{sinceShort(c.created_at)}</div>
                    </div>
                    {i === 0 ? null : (
                      <button
                        type="button"
                        className="btn-danger"
                        disabled={pending}
                        onClick={() => rollback(c.id)}
                      >
                        Rollback to this
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </aside>

      {publishOpen ? (
        <PublishDialog
          scopeKind="product_variant"
          scopeRef={`${product}/${activeVariant}`}
          configName={name.trim()}
          configYAML={yaml}
          targetingHint={`product=${product} · variant=${activeVariant}`}
          onClose={() => setPublishOpen(false)}
          onPublished={() => {
            setPublishOpen(false);
            setOk(true);
            onChanged();
            setTimeout(() => setOk(false), 1500);
          }}
        />
      ) : null}
    </>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Publish dialog — v1.0 modal that renders the Rollout primitive.
//
// See docs/v1.0-publish-dialog-spec.md. Two-pane layout: blast radius +
// validation on the left, rollout configuration on the right. Phased is
// the default; Instant requires explicit selection. On Start the dialog
// posts to /api/v1/rollouts and closes on success. The "transform in
// place to live progress view" + Pause/Abort/Fast-Promote affordances
// are deferred to a follow-up slice that builds the Rollouts UI section
// — this slice ships create-flow only.
// ─────────────────────────────────────────────────────────────────────────────

// extractYAMLLineContext pulls the lines around the error site from the
// YAML buffer. Many structural errors include a "line N" reference; for
// those, showing ±2 lines lets the operator see the malformed shape
// without scrolling back to the editor — the bug is often a line or two
// above where the parser detected it.
function extractYAMLLineContext(yaml: string, error: string): { lineNo: number; lines: { n: number; text: string; isErr: boolean }[] } | null {
  const m = error.match(/line (\d+)/);
  if (!m) return null;
  const lineNo = parseInt(m[1], 10);
  if (!Number.isFinite(lineNo) || lineNo < 1) return null;
  const all = yaml.split("\n");
  if (lineNo > all.length) return null;
  const start = Math.max(0, lineNo - 3);
  const end = Math.min(all.length, lineNo + 2);
  const lines = all.slice(start, end).map((text, i) => ({
    n: start + i + 1,
    text,
    isErr: start + i + 1 === lineNo,
  }));
  return { lineNo, lines };
}

// formatValidationError reshapes the otelcol loader's noisy stderr (or
// the shorter structural error) into something an operator can scan at
// a glance. The loader spits out a wrapper preamble ("failed to get
// config: cannot unmarshal..."), the actual issue(s), then a duplicate
// "collector server run finished with error: <same>" trailing line with
// a timestamp prefix — we strip the noise, extract each `error decoding
// '<section>': <reason>` line, and friendly-format known patterns
// (unknown component type, etc.). The raw text is preserved in a
// collapsed details panel for diagnostic recall.
function formatValidationError(raw: string): {
  headline: string;
  issues: string[];
  rawClean: string;
} {
  const cleaned = raw
    .replace(/^semantic validate failed:\s*Error:\s*/i, "")
    .replace(/^\s*failed to get config:\s*/i, "")
    .replace(/^\s*cannot unmarshal the configuration:\s*decoding failed due to the following error\(s\):\s*\n?/im, "")
    // Trailing duplicate that otelcol's main() logs.
    .replace(/^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2} collector server run finished with error:.*$/gm, "")
    .trim();

  // otelcol can nest errors: an outer "error decoding 'exporters':
  // decoding failed due to the following error(s):" line is just a
  // wrapper announcing that nested errors follow. The actionable detail
  // is on the next "error decoding '<sub>':" line. Walk lines tracking
  // the wrapper chain so leaves get formatted as "parent > child:
  // reason" and wrappers never emit as standalone bullets.
  const WRAPPER_REASON = /^decoding failed due to the following error\(s\):/i;
  const lines = cleaned.split("\n").map((l) => l.trim()).filter(Boolean);
  const issues: string[] = [];
  let wrapperPath: string[] = [];
  for (const line of lines) {
    // Primary leaf/wrapper shape: error decoding '<section>': <reason>
    const m = line.match(/^error decoding '([^']+)':\s*(.+)$/);
    if (m) {
      const section = m[1];
      let reason = m[2];
      if (WRAPPER_REASON.test(reason)) {
        wrapperPath.push(section);
        continue;
      }
      // Friendly: "unknown type: \"X\" ... (valid values: [a b c])"
      // → "Unknown component type \"X\". Supported: a, b, c."
      const unknownType = reason.match(/^unknown type:\s*"([^"]+)"(?:\s+for id:\s*"[^"]+")?\s*(?:\(valid values:\s*\[([^\]]+)\])?/);
      if (unknownType) {
        const typ = unknownType[1];
        const validList = unknownType[2];
        const valid = validList ? validList.split(/\s+/).filter(Boolean).join(", ") : null;
        reason = valid
          ? `Unknown component type "${typ}". Supported: ${valid}.`
          : `Unknown component type "${typ}".`;
      }
      issues.push(`${[...wrapperPath, section].join(" › ")}: ${reason}`);
      continue;
    }
    // Alternate leaf shape under a wrapper: '[<component>]' <reason>
    // (e.g. "'[otlphttp/backend]' expected a map, got 'string'" — operator
    // wrote a scalar value where the component expects nested config).
    if (wrapperPath.length > 0) {
      const altLeaf = line.match(/^'\[([^\]]+)\]'\s+(.+)$/);
      if (altLeaf) {
        let reason = altLeaf[2];
        // Friendly: "expected a map, got 'string'" is otelcol-speak for
        // "the operator wrote a scalar value where the component config
        // expects nested keys" — translate to operator language.
        if (/^expected a map, got '/.test(reason)) {
          reason = "expected a config map (nested keys under it), got a single value on the same line";
        }
        issues.push(`${[...wrapperPath, altLeaf[1]].join(" › ")}: ${reason}`);
        continue;
      }
      // Unknown leaf shape under a wrapper — pass it through verbatim
      // with the wrapper path so operators still see context.
      if (line.length < 240) {
        issues.push(`${wrapperPath.join(" › ")}: ${line}`);
        continue;
      }
    }
    // Anything else breaks the chain.
    wrapperPath = [];
  }
  const deduped = Array.from(new Set(issues));
  // No structured issues extracted? If the cleaned text is short enough to
  // be readable as one line (typical for structural errors like
  // "pipeline 'traces' has no receivers"), surface it directly.
  if (deduped.length === 0 && cleaned.length > 0 && cleaned.length < 240) {
    deduped.push(cleaned);
  }
  const headline = deduped.length > 1
    ? `Your config can't be applied (${deduped.length} issues):`
    : "Your config can't be applied:";
  return { headline, issues: deduped, rawClean: cleaned || raw };
}

function PublishDialog({
  scopeKind,
  scopeRef,
  configName,
  configYAML,
  targetingHint,
  onClose,
  onPublished,
}: {
  scopeKind: "product_variant" | "instance";
  scopeRef: string;
  configName: string;
  configYAML: string;
  targetingHint: string;
  onClose: () => void;
  onPublished: (rollout: import("@/lib/api").Rollout) => void;
}) {
  // Default to Instant rollout — publish reaches all hosts in scope at once,
  // no canary/soak/gate ceremony. Phased remains available behind the
  // Advanced toggle below for the rare case operators want graduated
  // rollout, but the everyday flow is single-knob: name, YAML, publish.
  // (Reversal of the v1.0-decisions.md default; the canary primitive
  // stays in the backend for when it's wanted, just not in the default
  // UI path.)
  const [kind, setKind] = useState<"phased" | "instant">("instant");
  const [pct, setPct] = useState<number>(5);
  const [count, setCount] = useState<number>(10);
  const [soakMinutes, setSoakMinutes] = useState<number>(5);
  const [gateMode, setGateMode] = useState<"auto" | "manual">("auto");
  const [advancedOpen, setAdvancedOpen] = useState<boolean>(false);

  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [validateError, setValidateError] = useState<string | null>(null);
  const [inFlight, setInFlight] = useState<{ id: number; state: string } | null>(null);

  useEffect(() => {
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = prev; };
  }, []);

  async function startRollout() {
    setPending(true);
    setError(null);
    setValidateError(null);
    setInFlight(null);
    try {
      const r = await api.createRollout({
        scope_kind: scopeKind,
        scope_ref: scopeRef,
        config: { name: configName || `${scopeRef.replace("/", "-")}-v1`, yaml: configYAML },
        rollout_kind: kind,
        canary: kind === "phased" ? { pct, count } : undefined,
        soak_seconds: kind === "phased" ? soakMinutes * 60 : undefined,
        gate_mode: kind === "phased" ? gateMode : undefined,
      });
      onPublished(r);
    } catch (err) {
      // Typed errors get inline rendering; everything else falls into the
      // generic banner. Spec §13 calls out the in-flight + validate
      // surfaces specifically because they're recoverable from inside
      // the dialog (operator can cancel, or fix YAML and retry).
      if (err instanceof RolloutInFlightError) {
        setInFlight({ id: err.existingRolloutId, state: err.existingState });
      } else if (err instanceof RolloutValidateError) {
        setValidateError(err.detail);
      } else {
        setError(err instanceof Error ? err.message : "Failed");
      }
    } finally {
      setPending(false);
    }
  }

  // Summary line at the dialog footer. Default flow (Instant) reads plainly
  // — no warnings, no ceremony language — since Instant is the normal mode.
  // Phased gets the descriptive line so operators who opted into Advanced
  // see exactly what they configured.
  const summary = kind === "phased"
    ? `Phased rollout: canary ${count}+ host(s) → soak ${soakMinutes} min → promote remaining.`
    : `Publishes the config to all hosts in this scope.`;

  return (
    <>
      <div className="modal-scrim" onClick={pending ? undefined : onClose} aria-hidden />
      <div role="dialog" aria-modal="true" className="modal-wrap" onClick={pending ? undefined : onClose}>
        <div
          className="card p-6 md:p-7"
          style={{ maxWidth: 880, width: "100%" }}
          onClick={(e) => e.stopPropagation()}
        >
          <div className="flex items-baseline justify-between gap-4 mb-3">
            <div>
              <div className="eyebrow">Publish</div>
              <h2 className="text-[22px] font-light tracking-tight" style={{ fontFamily: "var(--font-serif)" }}>
                {scopeRef}
              </h2>
            </div>
            <button type="button" className="btn-ghost" onClick={onClose} disabled={pending}>Close</button>
          </div>

          {/* Inline error states — spec §13 + §12. Each surfaces the
              specific recoverable error class so the operator can act
              without losing the configured rollout shape. */}
          {inFlight ? (
            <div className="banner-notice mb-4">
              <strong>Already in flight: rollout #{inFlight.id} ({inFlight.state}).</strong>{" "}
              Abort or wait for it to complete before publishing again.
            </div>
          ) : null}
          {validateError ? (() => {
            const fmt = formatValidationError(validateError);
            const ctx = extractYAMLLineContext(configYAML, validateError);
            return (
              <div className="banner-error mb-4">
                <div className="text-sm" style={{ color: "inherit" }}>
                  {fmt.headline}
                </div>
                {fmt.issues.length > 0 ? (
                  <ul
                    className="text-sm mt-2 space-y-1"
                    style={{ paddingLeft: 20, listStyle: "disc" }}
                  >
                    {fmt.issues.map((iss, i) => (
                      <li key={i}>{iss}</li>
                    ))}
                  </ul>
                ) : null}
                {ctx ? (
                  <div className="mt-3">
                    <div className="eyebrow mb-1" style={{ color: "inherit", opacity: 0.8 }}>
                      Your YAML near line {ctx.lineNo}
                    </div>
                    <pre
                      className="code"
                      style={{
                        background: "rgba(0,0,0,0.04)",
                        padding: "8px 10px",
                        borderRadius: 4,
                        fontSize: 12,
                        whiteSpace: "pre",
                        overflowX: "auto",
                        margin: 0,
                      }}
                    >
                      {ctx.lines.map((l) => (
                        <div
                          key={l.n}
                          style={{
                            background: l.isErr ? "rgba(200, 0, 0, 0.12)" : undefined,
                            fontWeight: l.isErr ? 600 : 400,
                          }}
                        >
                          <span style={{ opacity: 0.5, marginRight: 8 }}>
                            {String(l.n).padStart(3, " ")}
                          </span>
                          {l.isErr ? "→ " : "  "}
                          {l.text || " "}
                        </div>
                      ))}
                    </pre>
                    <p className="hint" style={{ marginTop: 6, color: "inherit", opacity: 0.8 }}>
                      The parser flagged this line, but the actual bug is sometimes a line or two above (e.g. a key with both a value and nested children).
                    </p>
                  </div>
                ) : null}
                <details className="mt-3">
                  <summary
                    style={{ cursor: "pointer", color: "inherit", opacity: 0.7, fontSize: 12 }}
                  >
                    Show raw output
                  </summary>
                  <div
                    className="code mt-2"
                    style={{ whiteSpace: "pre-wrap", opacity: 0.7, fontSize: 11 }}
                  >
                    {fmt.rawClean}
                  </div>
                </details>
                <p className="hint" style={{ marginTop: 10, color: "inherit" }}>
                  Close this dialog, fix the YAML in the editor, and retry.
                </p>
              </div>
            );
          })() : null}
          {error ? <div className="banner-error mb-4">{error}</div> : null}

          {/* Two-pane layout per spec §3. Blast radius left, rollout
              configuration right. At narrower viewports the panes stack. */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
            <section className="card px-4 py-4">
              <div className="eyebrow mb-2">Blast radius</div>
              <div className="text-sm">
                <div className="font-medium">{scopeRef}</div>
                <div className="text-[var(--color-muted)] mt-1">{targetingHint}</div>
              </div>
              <p className="text-xs text-[var(--color-muted)] mt-4">
                Magpie checks your YAML is well-formed and the pipelines reference
                receivers/exporters you've defined. Anything otelcol itself accepts is fair game.
              </p>
            </section>

            <section className="card px-4 py-4">
              <div className="eyebrow mb-2">Rollout</div>
              <p className="text-sm text-[var(--color-muted)]">
                {kind === "instant"
                  ? "Publishes immediately to all hosts in this scope."
                  : `Phased: canary ${count}+ host(s) → soak ${soakMinutes} min → promote remaining.`}
              </p>

              {/* Advanced controls — collapsed by default. Operators who want
                  graduated rollout (canary/soak/gate) toggle this open; the
                  default flow stays a one-knob publish. */}
              <button
                type="button"
                className="btn-ghost text-xs mt-3"
                onClick={() => setAdvancedOpen((v) => !v)}
                aria-expanded={advancedOpen}
              >
                {advancedOpen ? "Hide advanced ▴" : "Advanced ▾"}
              </button>

              {advancedOpen ? (
                <div className="mt-3 pt-3 border-t border-[var(--color-rule-soft)] space-y-4">
                  <div>
                    <div className="eyebrow mb-2">Rollout kind</div>
                    <div className="seg" role="radiogroup" aria-label="Rollout kind">
                      <button type="button" role="radio" aria-checked={kind === "instant"}
                        aria-pressed={kind === "instant"} onClick={() => setKind("instant")}>
                        Instant (default)
                      </button>
                      <button type="button" role="radio" aria-checked={kind === "phased"}
                        aria-pressed={kind === "phased"} onClick={() => setKind("phased")}>
                        Phased
                      </button>
                    </div>
                  </div>

                  {kind === "phased" ? (
                    <>
                      <div>
                        <div className="eyebrow mb-2">Canary</div>
                        <div className="grid grid-cols-2 gap-3">
                          <label className="block">
                            <span className="text-xs text-[var(--color-muted)]">Percent</span>
                            <input
                              type="number" className="field mt-1.5"
                              value={pct} min={1} max={50}
                              onChange={(e) => setPct(Math.max(1, Math.min(50, Number(e.target.value) || 0)))}
                            />
                          </label>
                          <label className="block">
                            <span className="text-xs text-[var(--color-muted)]">At least N hosts</span>
                            <input
                              type="number" className="field mt-1.5"
                              value={count} min={1}
                              onChange={(e) => setCount(Math.max(1, Number(e.target.value) || 0))}
                            />
                          </label>
                        </div>
                        <p className="hint" style={{ marginTop: 6 }}>
                          Effective canary is the larger of {pct}% of connected and {count}.
                        </p>
                      </div>

                      <div>
                        <div className="eyebrow mb-2">Soak window</div>
                        <div className="flex items-center gap-2">
                          <input
                            type="number" className="field" style={{ maxWidth: 90 }}
                            value={soakMinutes} min={1} max={60}
                            onChange={(e) => setSoakMinutes(Math.max(1, Math.min(60, Number(e.target.value) || 0)))}
                          />
                          <span className="text-sm text-[var(--color-muted)]">minutes</span>
                        </div>
                        {soakMinutes > 30 ? (
                          <p className="hint" style={{ marginTop: 6 }}>
                            Long soaks tie up the rollout slot for this scope. Consider 30 min max.
                          </p>
                        ) : null}
                      </div>

                      <div>
                        <div className="eyebrow mb-2">Gate</div>
                        <div className="seg" role="radiogroup" aria-label="Gate mode">
                          <button type="button" role="radio" aria-checked={gateMode === "auto"}
                            aria-pressed={gateMode === "auto"} onClick={() => setGateMode("auto")}>
                            Auto
                          </button>
                          <button type="button" role="radio" aria-checked={gateMode === "manual"}
                            aria-pressed={gateMode === "manual"} onClick={() => setGateMode("manual")}>
                            Manual
                          </button>
                        </div>
                        <p className="hint" style={{ marginTop: 6 }}>
                          {gateMode === "auto"
                            ? "Advances when canary is healthy and soak completes."
                            : "Holds at \"ready to promote\" after soak; you Promote explicitly."}
                        </p>
                      </div>
                    </>
                  ) : null}
                </div>
              ) : null}
            </section>
          </div>

          <div className="border-t border-[var(--color-rule-soft)] pt-4 flex items-center justify-between gap-4 flex-wrap">
            <span className="text-xs text-[var(--color-muted)]" style={{ maxWidth: 460 }}>
              {summary}
            </span>
            <div className="flex items-center gap-2">
              <button type="button" className="btn-ghost" onClick={onClose} disabled={pending}>
                Cancel
              </button>
              <button type="button" className="btn" onClick={startRollout} disabled={pending}>
                {pending ? "Publishing…" : "Publish ▸"}
              </button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Agent detail drawer — health, attributes, label override
// ─────────────────────────────────────────────────────────────────────────────

function AgentDetailDrawer({
  agent,
  products,
  grouped,
  rollouts,
  controlPlaneVersion,
  onClose,
  onChanged,
  onOpenProduct,
  onOpenRollout,
  onViewFullAudit,
}: {
  agent: Agent;
  products: string[];
  grouped: Grouped;
  rollouts: Rollout[] | null;
  controlPlaneVersion: string | null;
  onClose: () => void;
  onChanged: () => void;
  onOpenProduct: (product: string) => void;
  onOpenRollout: (rollout: Rollout) => void;
  onViewFullAudit: (hostUid: string) => void;
}) {
  useEffect(() => {
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = prev; };
  }, []);

  const host = agent.attributes?.["host.name"] ?? agent.attributes?.["service.instance.id"] ?? agent.instance_uid.slice(0, 16);
  const advertised = agentLabels(agent);
  const effective = {
    product: agent.effective_product ?? advertised.product,
    variant: agent.effective_variant ?? advertised.variant,
  };
  const overrideActive = Boolean(agent.label_override);

  const [prod, setProd] = useState(effective.product);
  const [variant, setVariant] = useState(effective.variant);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  // Host-scoped audit slice (spec §8). Loaded lazily via /api/v1/audit
  // pre-filtered to host_ref = this instance_uid. Five rows here; full
  // history is one click away via the footer link → Audit section.
  // null = still loading; [] = loaded-empty (no v1.0 host-tagged events
  // for this host yet); array = events to render.
  const [recentEvents, setRecentEvents] = useState<AuditEntry[] | null>(null);
  const [recentEventsErr, setRecentEventsErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setRecentEvents(null);
    setRecentEventsErr(null);
    api.listAudit({ host_ref: agent.instance_uid }, 5)
      .then((rows) => { if (!cancelled) setRecentEvents(rows); })
      .catch((err) => {
        if (cancelled) return;
        setRecentEventsErr(err instanceof Error ? err.message : "Failed");
      });
    return () => { cancelled = true; };
  }, [agent.instance_uid]);

  // In-flight rollouts that touch this host (spec §6, host-drawer zone 4).
  // Hidden entirely when zero. The page-level rollouts poll feeds the
  // candidate set; for each non-terminal rollout we fetch apply_state
  // restricted to this instance_uid (server-side host_ref filter) so
  // per-poll payload is one row per rollout. The effect re-fires on every
  // page poll, keeping per-host applyState (pending → applying → applied)
  // fresh alongside the phase pill.
  const inFlightRollouts = useMemo(
    () => (rollouts ?? []).filter((r) => r.state !== "done" && r.state !== "aborted"),
    [rollouts],
  );
  const [hostInFlight, setHostInFlight] = useState<
    Array<{ rollout: Rollout; applyState: ApplyState }>
  >([]);
  useEffect(() => {
    let cancelled = false;
    if (inFlightRollouts.length === 0) {
      setHostInFlight([]);
      return;
    }
    Promise.all(
      inFlightRollouts.map(async (r) => {
        try {
          // Limit=2 is paranoia — there can be at most one row per
          // (rollout_id, instance_uid). The host_ref filter does the
          // real work of keeping the response payload tiny.
          const rows = await api.listApplyState(r.id, 2, undefined, agent.instance_uid);
          const row = rows.find((s) => s.instance_uid === agent.instance_uid);
          return row ? { rollout: r, applyState: row } : null;
        } catch {
          // Best-effort: if a single rollout's apply-state fetch fails,
          // don't blank the whole strip. The other rollouts still render.
          return null;
        }
      }),
    ).then((results) => {
      if (cancelled) return;
      setHostInFlight(
        results.filter(
          (x): x is { rollout: Rollout; applyState: ApplyState } => x !== null,
        ),
      );
    });
    return () => { cancelled = true; };
  }, [agent.instance_uid, inFlightRollouts]);

  const status = configStatusLabel(agent.config_status);

  // v1.0 diagnostic state — computed for the diagnostic-led header. Order
  // matters: failed beats unhealthy beats drifting beats pending beats
  // applied. Drifting detection is the one piece this slice approximates;
  // a future slice with sha256-aware live-hash comparison sharpens it.
  // For now, "drifting" surfaces only via the applied-vs-live YAML name
  // comparison — the proper hash diff comes when the rollouts UI exposes
  // live-config hashes.
  const liveConfig = grouped[effective.product]?.[effective.variant]?.[0];
  type Diag = "applied" | "drifting" | "failed" | "unhealthy" | "pending";
  let diag: Diag = "applied";
  if (agent.config_status === "failed") diag = "failed";
  else if (agent.healthy === false) diag = "unhealthy";
  else if (!agent.applied_config_hash) diag = "pending";
  // drifting detection deferred to a follow-up slice.

  // Stale `last_seen` colouring per spec §7: amber > 5 min, red > 1 hour.
  // Drives the freshness pill on the Health summary so operators don't
  // act on hours-old data thinking it's current.
  const lastSeenStaleness = stalenessOf(agent.last_seen);

  // Agent binary upgrades are manual — OpAMP AcceptsPackages / PackagesAvailable
  // isn't implemented on either side yet (see LIMITATIONS.md). Surface the
  // version drift explicitly so operators don't stare at an agent that's
  // silently running an old build because "update" only covers config.
  const agentVersion = agent.attributes?.["service.version"] ?? "";
  const agentOS = agent.attributes?.["os.type"] ?? "";
  const agentArch = agent.attributes?.["host.arch"] ?? "";
  const versionKnown = agentVersion !== "";
  const versionsDiffer =
    controlPlaneVersion !== null &&
    (!versionKnown || agentVersion !== controlPlaneVersion);
  const downloadable = Boolean(agentOS && agentArch);
  async function downloadAgentZip() {
    if (!agentOS || !agentArch) return;
    try {
      const blob = await api.downloadReleaseZip(agentOS, agentArch);
      saveBlob(blob, `magpie-${agentOS}-${agentArch}.zip`);
    } catch (err) {
      // Errors are rare here (auth missing → AuthRequiredError surfaces a
      // modal globally; 404 means catalog/agent-attribute drift). Either
      // way we can't render a useful message in this drawer; let the
      // global error handling cope.
      console.error("download agent zip failed", err);
    }
  }

  async function saveOverride() {
    setPending(true);
    setError(null);
    try {
      await api.setAgentLabels(agent.instance_uid, prod.trim(), variant.trim());
      setOk("Override saved. Agent will pick up the matching config.");
      onChanged();
      setTimeout(() => setOk(null), 2000);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed");
    } finally {
      setPending(false);
    }
  }

  async function clearOverride() {
    setPending(true);
    setError(null);
    try {
      await api.clearAgentLabels(agent.instance_uid);
      setOk("Override cleared. Using the agent's advertised labels again.");
      onChanged();
      setTimeout(() => setOk(null), 2000);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed");
    } finally {
      setPending(false);
    }
  }

  // Delete this host's record from the registry. Use cases: clearing
  // duplicate/stale rows left by older agents that generated a fresh
  // InstanceUid on every restart, or removing a decommissioned host.
  // A still-running agent will simply re-register on next heartbeat
  // (and now lands back in the same uid since InstanceUid is derived
  // from a stable per-machine seed).
  async function deleteHost() {
    if (!window.confirm(`Delete this host record?\n\n${host}\n\nIf the agent is still running it will re-register on its next heartbeat. Use this to clean up stale duplicate rows.`)) {
      return;
    }
    setPending(true);
    setError(null);
    try {
      await api.deleteAgent(agent.instance_uid);
      onChanged();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed");
      setPending(false);
    }
  }

  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      <aside role="dialog" aria-modal="true" className="drawer">
        <header className="px-7 py-5 border-b border-[var(--color-rule)] flex items-baseline justify-between gap-4">
          <div className="min-w-0">
            <div className="eyebrow">Host</div>
            <h2 className="mt-1 text-[22px] font-light tracking-tight truncate" style={{ fontFamily: "var(--font-serif)" }}>
              {host}
            </h2>
          </div>
          <button type="button" className="btn-ghost" onClick={onClose}>Close</button>
        </header>

        <div className="flex-1 min-h-0 overflow-y-auto px-7 py-5">
          {error ? <div className="banner-error mb-4">{error}</div> : null}
          {ok ? (
            <div className="banner-success mb-4">
              {ok}
            </div>
          ) : null}

          {/* v1.0 spec §4: diagnostic-led header. The first thing the
              operator's eye lands on is *what's wrong* (or "everything's
              fine") plus the primary action buttons — not section
              headers, not ID fields. */}
          <DiagnosticBlock
            diag={diag}
            configError={agent.config_error}
            lastApplied={agent.last_status === "running" ? sinceShort(agent.last_seen) : null}
            onOpenConfig={() => onOpenProduct(effective.product)}
          />

          {/* v1.0 spec §5: Scope resolution chain — the keystone of this
              surface. Three stops (instance > product+variant > default);
              the arrow points at the active scope. Inactive entries are
              greyed but visible so the operator sees what would take over
              if the active one were cleared. */}
          <ScopeResolution
            agent={agent}
            advertised={advertised}
            effective={effective}
            liveConfig={liveConfig}
          />

          {/* v1.0 spec §6: In-flight rollouts (this host). Hidden when the
              host isn't part of any in-flight rollout — typical case. When
              it is, render up to 3 cards with phase, role (canary/promote
              from is_canary), and apply state. Spec is conservative on
              count: this section caps at 3 cards plus "+N more" if more
              than 3 in-flight rollouts touch this host. */}
          {hostInFlight.length > 0 ? (
            <section className="mb-6" aria-live="polite">
              <div className="eyebrow mb-2">
                In-flight rollouts ({hostInFlight.length})
              </div>
              <div className="space-y-2">
                {hostInFlight.slice(0, 3).map(({ rollout, applyState }) => (
                  <HostInFlightRolloutCard
                    key={rollout.id}
                    rollout={rollout}
                    applyState={applyState}
                    hostUID={agent.instance_uid}
                    onOpen={() => onOpenRollout(rollout)}
                    onRepushed={onChanged}
                  />
                ))}
                {hostInFlight.length > 3 ? (
                  <div className="text-xs text-[var(--color-muted)] px-1">
                    +{hostInFlight.length - 3} more — see Rollouts section
                  </div>
                ) : null}
              </div>
            </section>
          ) : null}

          {/* Health summary — compact, four lines, no buttons. The
              diagnostic block above already surfaced the load-bearing
              state; this is reference data. Last-seen tinted amber/red
              when stale per spec §7. */}
          <section className="mb-6">
            <div className="eyebrow mb-2">Health</div>
            {/* When the agent has no open OpAMP WebSocket right now, surface
                that explicitly — the rest of the health fields are from the
                last persisted heartbeat and would otherwise mislead operators
                into thinking the agent is live. */}
            {agent.connected === false ? (
              <div className="banner-error mb-3">
                <strong>Disconnected.</strong> The agent does not have an open OpAMP connection.
                Health fields below are from the last successful heartbeat ({sinceShort(agent.last_seen)})
                and may be stale.
              </div>
            ) : null}
            <div className="grid grid-cols-2 gap-3 text-sm">
              <KV label="OpAMP">
                <span className={agent.connected ? "pill pill-ok" : "pill pill-bad"}>
                  {agent.connected ? "connected" : "disconnected"}
                </span>
              </KV>
              <KV label="Healthy">
                <span className={agent.healthy === true ? "pill pill-ok" : agent.healthy === false ? "pill pill-bad" : "pill"}>
                  {agent.healthy === true ? "yes" : agent.healthy === false ? "no" : "not yet reported"}
                </span>
              </KV>
              <KV label="Connected at">
                <span className="text-[var(--color-muted)]">{sinceShort(agent.connected_at)}</span>
              </KV>
              <KV label="Last seen">
                <span style={{
                  color: lastSeenStaleness === "red"
                    ? "var(--color-danger)"
                    : lastSeenStaleness === "amber"
                      ? "#caa247"
                      : "var(--color-muted)",
                }}>
                  {sinceShort(agent.last_seen)}
                </span>
              </KV>
            </div>
            {agent.config_error && diag !== "failed" ? (
              <div className="mt-3 banner-error">
                <div className="eyebrow mb-1" style={{ color: "inherit" }}>Last config apply error</div>
                <div className="code" style={{ whiteSpace: "pre-wrap" }}>{agent.config_error}</div>
              </div>
            ) : null}
          </section>

          {/* v1.0 spec §8: Recent activity (host-scoped). Last 5 events
              filtered by host_ref = this instance_uid, with a footer link
              into the full Audit section pre-filtered to this host. Pre-v1.0
              audit rows aren't host-tagged, so an empty result is normal
              for a host whose only history is from before v1.0 — say so
              explicitly rather than show a misleading "no activity." */}
          <section className="mb-6" aria-live="polite">
            <div className="eyebrow mb-2">Recent activity (this host)</div>
            {recentEventsErr ? (
              <div className="banner-error">
                Couldn&apos;t load recent activity for this host: {recentEventsErr}
              </div>
            ) : recentEvents === null ? (
              <div className="card px-4 py-3 text-xs text-[var(--color-muted)]">Loading…</div>
            ) : recentEvents.length === 0 ? (
              <div className="card px-4 py-3 text-sm text-[var(--color-muted)]">
                No recorded activity. Events from before v1.0 may not be host-tagged.
              </div>
            ) : (
              <ul className="card divide-y divide-[var(--color-rule-soft)]">
                {recentEvents.map((e) => {
                  const isLegacy = !e.hash;
                  const typeLabel = e.type || (isLegacy ? "legacy" : e.action);
                  const summary = summariseAudit(e);
                  return (
                    <li key={e.id} className="px-4 py-2.5">
                      <div className="flex items-baseline justify-between gap-3 text-sm">
                        <div className="min-w-0 flex-1">
                          <div className="flex items-baseline gap-2 flex-wrap">
                            <span className="pill" style={{ fontSize: 11, color: dotColorFor(e) }}>
                              {typeLabel}
                            </span>
                            <span className="text-[var(--color-muted)] text-xs">{e.actor}</span>
                            {e.scope_ref ? (
                              <span className="code text-xs text-[var(--color-muted)]">
                                {e.scope_kind === "instance" ? "this host" : e.scope_ref}
                              </span>
                            ) : null}
                          </div>
                          {summary ? (
                            <div className="text-xs text-[var(--color-muted)] mt-0.5 truncate">{summary}</div>
                          ) : null}
                        </div>
                        <span className="text-xs text-[var(--color-muted-soft)] whitespace-nowrap shrink-0">
                          {sinceShort(e.at)}
                        </span>
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
            {recentEvents && recentEvents.length > 0 ? (
              <div className="mt-2 flex justify-end">
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() => onViewFullAudit(agent.instance_uid)}
                >
                  View all events for this host in Audit ▸
                </button>
              </div>
            ) : null}
          </section>

          {/* Agent version drift — surface manual-upgrade requirement.
              Config updates hot-reload over OpAMP, but the agent binary
              itself does not; operators need to re-download + reinstall. */}
          {versionsDiffer ? (
            <section className="mb-6">
              <div className="eyebrow mb-2">Agent version</div>
              <div className="banner-notice">
                <div className="flex items-baseline justify-between gap-4 flex-wrap">
                  <div>
                    <strong>Manual agent upgrade available.</strong>{" "}
                    Host reports{" "}
                    <span className="code">{versionKnown ? agentVersion : "unknown"}</span>
                    {" · control plane is "}
                    <span className="code">{controlPlaneVersion}</span>.
                  </div>
                  {downloadable ? (
                    <button type="button" className="btn" onClick={downloadAgentZip}>
                      Download {agentOS}-{agentArch}
                    </button>
                  ) : (
                    <span className="text-xs text-[var(--color-muted)]">
                      OS/arch not reported; see docs/onboarding.md
                    </span>
                  )}
                </div>
                <p className="hint" style={{ marginTop: 8 }}>
                  Agent binaries upgrade by reinstalling on the host — Magpie doesn&apos;t push
                  binaries over OpAMP yet. Extract next to the existing install and restart the
                  service; config will hot-reload automatically.
                </p>
              </div>
            </section>
          ) : null}

          {/* v1.0 spec §9: Re-assign cohort (label override). Carryover
              from v0.2; preserves the affordance for moving a host
              between existing cohorts without SSH. */}
          <section className="mb-6">
            <div className="flex items-baseline justify-between mb-2">
              <div className="eyebrow">Re-assign to a different cohort</div>
              {overrideActive ? <span className="pill" style={{ fontSize: 10 }}>override active</span> : null}
            </div>
            {overrideActive ? (
              <p className="hint mb-3" style={{ marginTop: 0 }}>
                Advertised: <span className="code">{advertised.product} · {advertised.variant}</span>
                <span className="mx-2">→</span>
                Effective: <span className="code">{effective.product} · {effective.variant}</span>
              </p>
            ) : (
              <p className="hint mb-3" style={{ marginTop: 0 }}>
                This host advertises{" "}
                <span className="code">{advertised.product} · {advertised.variant}</span>.
                Override saves a label-only reassignment — config follows the new cohort on next resolve.
              </p>
            )}
            <div className="card px-4 py-4">
              <div className="grid grid-cols-2 gap-3">
                <label className="block">
                  <span className="eyebrow">Product</span>
                  <input
                    list="agent-products"
                    className="field mt-1.5 code"
                    value={prod}
                    onChange={(e) => setProd(e.target.value)}
                    spellCheck={false}
                  />
                  <datalist id="agent-products">
                    {products.map((p) => <option key={p} value={p} />)}
                  </datalist>
                </label>
                <label className="block">
                  <span className="eyebrow">Variant</span>
                  <input
                    className="field mt-1.5 code"
                    value={variant}
                    onChange={(e) => setVariant(e.target.value)}
                    spellCheck={false}
                  />
                </label>
              </div>
              <div className="mt-4 flex items-center justify-end gap-2">
                {overrideActive ? (
                  <button type="button" className="btn-ghost" disabled={pending} onClick={clearOverride}>
                    Clear override
                  </button>
                ) : null}
                <button
                  type="button"
                  className="btn"
                  disabled={pending || !prod.trim() || !variant.trim() || (prod === effective.product && variant === effective.variant && overrideActive)}
                  onClick={saveOverride}
                >
                  {pending ? "Saving…" : overrideActive ? "Update override" : "Apply override"}
                </button>
              </div>
            </div>
          </section>

          {/* v1.0 spec §10: Override this host's config (Shape 1). Distinct
              from the label override above — this gives the host its own
              per-host Config that's not bound to any product+variant cohort.
              UI for the publish dialog is a follow-up; this shows the
              affordance shape so operators see the v1.0 model and don't
              fall back to the v0.2 "spin up a cohort-of-one" workaround. */}
          <section className="mb-6">
            <div className="eyebrow mb-2">Override this host's config</div>
            <div className="card px-4 py-4 flex items-center justify-between gap-4">
              <p className="hint" style={{ marginTop: 0, maxWidth: "62ch" }}>
                This host follows{" "}
                <span className="code">{effective.product}/{effective.variant}</span>&apos;s config.
                To give it a custom config that&apos;s distinct from the cohort, override here.
                Creates an instance-scope Rollout targeting this host only.
              </p>
              <button
                type="button"
                className="btn-ghost"
                disabled
                title="Publish dialog UI is the next slice — backend already exposes POST /api/v1/rollouts with scope_kind=instance."
              >
                Override ▸
              </button>
            </div>
          </section>

          {/* Attributes dump */}
          <section className="mb-6">
            <div className="eyebrow mb-2">All attributes</div>
            <div className="card overflow-hidden">
              <table className="w-full text-sm">
                <tbody>
                  {Object.entries(agent.attributes ?? {}).sort().map(([k, v]) => (
                    <tr key={k} className="border-b border-[var(--color-rule-soft)] last:border-0">
                      <td className="px-4 py-2 code text-[var(--color-muted)] w-[45%]">{k}</td>
                      <td className="px-4 py-2 code">{v}</td>
                    </tr>
                  ))}
                  <tr>
                    <td className="px-4 py-2 code text-[var(--color-muted)]">instance_uid</td>
                    <td className="px-4 py-2 code">{agent.instance_uid}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          {/* Danger zone — delete the host record. Primary use case is
              cleaning up duplicate rows from older agent builds whose
              InstanceUid was random per-restart. */}
          <section>
            <div className="eyebrow mb-2">Danger zone</div>
            <div className="card px-4 py-4 flex items-center justify-between gap-4">
              <div className="text-sm">
                <div><strong>Delete host record</strong></div>
                <p className="hint" style={{ marginTop: 4 }}>
                  Removes this row from the registry. If the agent is still running it will
                  re-register on its next heartbeat — useful for clearing stale duplicates.
                </p>
              </div>
              <button type="button" className="btn-danger" disabled={pending} onClick={deleteHost}>
                Delete host
              </button>
            </div>
          </section>
        </div>
      </aside>
    </>
  );
}

function KV({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="eyebrow">{label}</div>
      <div className="mt-1">{children}</div>
    </div>
  );
}

// DiagnosticBlock is the v1.0 host-drawer diagnostic-led header. Renders
// the four-state ✓/⚠/✗/⏳ summary at the top of the drawer body so the
// operator's first read is "what's the state of this host" — not a
// section heading.
function DiagnosticBlock({
  diag,
  configError,
  lastApplied,
  onOpenConfig,
}: {
  diag: "applied" | "drifting" | "failed" | "unhealthy" | "pending";
  configError?: string;
  lastApplied: string | null;
  onOpenConfig: () => void;
}) {
  const meta = (() => {
    switch (diag) {
      case "applied":
        return { glyph: "✓", label: "Applied", line: lastApplied ? `Last seen ${lastApplied}.` : null, tone: "ok" as const };
      case "drifting":
        return { glyph: "⚠", label: "Drifting", line: "Applied hash differs from this scope's live config.", tone: "warn" as const };
      case "failed":
        return { glyph: "✗", label: "Failed apply", line: configError || "The agent reported a config-apply failure.", tone: "bad" as const };
      case "unhealthy":
        return { glyph: "✗", label: "Unhealthy", line: "Agent reports it's not running collector pipelines correctly.", tone: "bad" as const };
      case "pending":
        return { glyph: "⏳", label: "Pending", line: "Agent connected but hasn't applied a config yet.", tone: "neutral" as const };
    }
  })();
  const color =
    meta.tone === "ok"   ? "var(--color-accent)" :
    meta.tone === "warn" ? "#caa247" :
    meta.tone === "bad"  ? "var(--color-danger)" :
    "var(--color-muted)";
  return (
    <section className="mb-6">
      <div className="flex items-baseline gap-2 mb-1">
        <span style={{ color, fontSize: 18, lineHeight: 1 }}>{meta.glyph}</span>
        <span className="text-[18px] font-medium" style={{ color }}>{meta.label}</span>
      </div>
      {meta.line ? (
        <div
          className={diag === "failed" ? "code text-sm" : "text-sm text-[var(--color-muted)]"}
          style={diag === "failed" ? { whiteSpace: "pre-wrap" } : undefined}
        >
          {meta.line}
        </div>
      ) : null}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" className="btn" onClick={onOpenConfig}>
          Open the config running here ▸
        </button>
      </div>
    </section>
  );
}

// ScopeResolution renders the three-stop chain that's the v1.0 host
// drawer's keystone (spec §5). Instance > product+variant > default,
// with the "↑ in force" arrow under whichever is currently active.
// Inactive entries are greyed but still visible so an operator who's
// about to clear an override sees what would take over.
//
// In this first slice the instance scope is always shown as "(none)" —
// Shape 1 per-host overrides aren't yet exposed via an Agent field, so
// we can't tell from the API whether a host has one. When that field
// lands, this component fills the instance slot from it.
function ScopeResolution({
  agent,
  advertised,
  effective,
  liveConfig,
}: {
  agent: Agent;
  advertised: { product: string; variant: string };
  effective: { product: string; variant: string };
  liveConfig: Config | undefined;
}) {
  // First-slice limitation — Shape 1 surfacing is deferred to a future
  // turn that adds instance-scope override fields to the Agent type.
  const inForce: "instance" | "product_variant" | "default" =
    liveConfig ? "product_variant" : "default";

  const cell = (
    label: string,
    body: React.ReactNode,
    active: boolean,
    muted: boolean,
  ) => (
    <div
      className="card px-4 py-3 flex-1"
      style={{
        background: active ? "var(--color-paper-warm)" : undefined,
        borderColor: active ? "var(--color-accent)" : undefined,
        opacity: muted ? 0.5 : 1,
      }}
    >
      <div className="eyebrow mb-1">{label}</div>
      <div className="code text-sm">{body}</div>
    </div>
  );

  const overrideActive = Boolean(agent.label_override);
  return (
    <section className="mb-6">
      <div className="eyebrow mb-2">Scope resolution</div>
      <div className="flex flex-col md:flex-row gap-2">
        {cell("instance", "(none)", false, true)}
        {cell(
          "product+variant",
          <>
            {effective.product} / {effective.variant}
            {overrideActive ? (
              <span className="text-[var(--color-muted)] ml-2 text-xs">
                (override; advertised {advertised.product}/{advertised.variant})
              </span>
            ) : null}
          </>,
          inForce === "product_variant",
          inForce !== "product_variant",
        )}
        {cell("default", "default / default", inForce === "default", inForce !== "default")}
      </div>
      <div className="mt-3 grid grid-cols-2 gap-3 text-sm">
        <KV label="Live config">
          {liveConfig ? (
            <span>
              <span>{liveConfig.name}</span>{" "}
              <span className="code text-xs text-[var(--color-muted)]">
                {shortHash(hashFor(liveConfig.yaml))}
              </span>
            </span>
          ) : (
            <span className="text-[var(--color-muted)]">— (no config published yet for this scope)</span>
          )}
        </KV>
        <KV label="Applied">
          <span className="code text-xs">
            {agent.applied_config_hash ? shortHash(agent.applied_config_hash) : "—"}
          </span>
        </KV>
      </div>
    </section>
  );
}

// stalenessOf classifies a `last_seen` timestamp into a heat-map bucket
// for the host drawer's freshness signal (spec §7). Operator should
// treat "amber" as "probably fine but watch" and "red" as "definitely
// not heartbeating, investigate."
function stalenessOf(lastSeenISO: string): "fresh" | "amber" | "red" {
  const t = Date.parse(lastSeenISO);
  if (!Number.isFinite(t)) return "fresh";
  const ageMs = Date.now() - t;
  if (ageMs >= 60 * 60 * 1000) return "red";
  if (ageMs >= 5 * 60 * 1000) return "amber";
  return "fresh";
}

// ─────────────────────────────────────────────────────────────────────────────
// New product modal — tiny, just prompts for a name and a starting variant
// ─────────────────────────────────────────────────────────────────────────────

function NewProductModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (product: string, variant: VariantKey) => void;
}) {
  const [product, setProduct] = useState("");
  const [variant, setVariant] = useState<VariantKey>("linux");
  return (
    <>
      <div className="modal-scrim" onClick={onClose} aria-hidden />
      <div
        role="dialog" aria-modal="true"
        className="modal-wrap"
        onClick={onClose}
      >
        <div className="card p-6 md:p-7" style={{ maxWidth: 460, width: "100%" }} onClick={(e) => e.stopPropagation()}>
          <div className="eyebrow">New product</div>
          <h2 className="mt-1 mb-4 text-[22px] font-light tracking-tight" style={{ fontFamily: "var(--font-serif)" }}>
            Create a product bucket
          </h2>
          <label className="block mb-3">
            <span className="eyebrow">Name</span>
            <input
              className="field mt-1.5 code"
              placeholder="ship"
              value={product}
              onChange={(e) => setProduct(e.target.value)}
              autoFocus
            />
          </label>
          <div className="mb-5">
            <span className="eyebrow">Starting variant</span>
            <div className="seg mt-1.5 flex-wrap">
              {VARIANTS.map((v) => (
                <button key={v.key} type="button" aria-pressed={variant === v.key} onClick={() => setVariant(v.key)}>
                  {v.label}
                </button>
              ))}
            </div>
          </div>
          <div className="flex items-center justify-end gap-2">
            <button type="button" className="btn-ghost" onClick={onClose}>Cancel</button>
            <button
              type="button"
              className="btn"
              disabled={!product.trim()}
              onClick={() => onCreated(product.trim(), variant)}
            >
              Continue
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Onboard modal (preserved from prior version, minor polish)
// ─────────────────────────────────────────────────────────────────────────────

// variantOS maps the configurable variant picker to the OS keys the release
// catalog uses. `kubernetes` renders a different panel (Helm) entirely, so it
// returns null. `custom` isn't tied to a host type — default to linux since
// that covers the majority of fleet hosts; users can re-pick in the arch
// picker below if a windows build is also available.
function variantOS(variant: string): "windows" | "linux" | null {
  if (variant === "windows") return "windows";
  if (variant === "linux") return "linux";
  if (variant === "kubernetes") return null;
  return "linux";
}

function formatBytes(n: number): string {
  if (n <= 0) return "";
  const mb = n / (1024 * 1024);
  if (mb >= 1) return `${mb.toFixed(1)} MB`;
  return `${(n / 1024).toFixed(0)} KB`;
}

function OnboardModal({
  target,
  onClose,
  suggestedVariants,
  onPickVariant,
}: {
  target: { product: string; variant: string };
  onClose: () => void;
  suggestedVariants: string[];
  onPickVariant: (v: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const [catalog, setCatalog] = useState<ReleaseCatalog | null>(null);
  const [catalogError, setCatalogError] = useState<string | null>(null);
  const [arch, setArch] = useState<string | null>(null);

  // Fetch the release catalog once — only used by the "Manual install"
  // expander. The primary curl/iwr path doesn't need it (the install
  // script handles os/arch detection itself).
  useEffect(() => {
    let alive = true;
    api.releases()
      .then((c) => { if (alive) setCatalog(c); })
      .catch((e: unknown) => {
        if (alive) setCatalogError(e instanceof Error ? e.message : String(e));
      });
    return () => { alive = false; };
  }, []);

  const targetOS = variantOS(target.variant);
  const isKubernetes = target.variant === "kubernetes";
  const isWindows = target.variant === "windows";

  // installBase is the URL the operator's host (NOT just the browser) will
  // hit. Falls back to window.location.origin when API is empty (i.e. the
  // UI is being served same-origin via a reverse proxy that already routes
  // /install.* to magpied). The operator copy-pastes this into a terminal
  // so it must be a fully-qualified URL.
  const installBase = typeof window !== "undefined"
    ? (API || window.location.origin)
    : "<MAGPIED_URL>";

  // Path lives under /api/v1 so any reverse proxy that already forwards
  // the API to magpied (the only stable contract across deployments)
  // forwards these too — root-level paths got 404'd by proxies that
  // only routed /api/* + /v1/opamp.
  const installURL = isWindows
    ? `${installBase}/api/v1/install.ps1?product=${encodeURIComponent(target.product)}&variant=${encodeURIComponent(target.variant)}`
    : `${installBase}/api/v1/install.sh?product=${encodeURIComponent(target.product)}&variant=${encodeURIComponent(target.variant)}`;

  // The one-liner. With v0.2 token auth on, the operator presents the
  // bearer token on the install request — but the rendered command refers
  // to $MAGPIE_API_TOKEN / $env:MAGPIE_API_TOKEN rather than the literal
  // value, so the secret never lands in shell history. The operator sets
  // it once (read -s on Linux, $env:MAGPIE_API_TOKEN = '…' on Windows)
  // before pasting. For v0.1-compatible (no-token) magpieds, the header
  // is omitted. For kubernetes we fall through to the Helm hint below
  // and never render this.
  const tokenInUI = typeof window !== "undefined" ? auth.getToken() : "";
  const oneLiner = isWindows
    ? tokenInUI
      ? `$h = @{ Authorization = "Bearer $env:MAGPIE_API_TOKEN" }; iwr -useb -Headers $h "${installURL}" | iex`
      : `iwr -useb "${installURL}" | iex`
    : tokenInUI
      ? `curl -fsSL -H "Authorization: Bearer $MAGPIE_API_TOKEN" "${installURL}" | sudo -E bash`
      : `curl -fsSL "${installURL}" | sudo bash`;

  // Platforms published for this OS — used by the Manual install expander
  // for the arch picker / size hint.
  const osPlatforms: ReleasePlatform[] = useMemo(() => {
    if (!catalog || !targetOS) return [];
    return catalog.platforms.filter((p) => p.os === targetOS);
  }, [catalog, targetOS]);

  useEffect(() => {
    if (osPlatforms.length === 0) { setArch(null); return; }
    const preferred = osPlatforms.find((p) => p.arch === "amd64") ?? osPlatforms[0];
    setArch(preferred.arch);
  }, [osPlatforms]);

  const chosenPlatform = useMemo(
    () => (arch ? osPlatforms.find((p) => p.arch === arch) ?? null : null),
    [osPlatforms, arch],
  );

  // Manual command shown inside the expander. The install script handles
  // the systemd-unit + Windows-Service-install paths; this is just the
  // foreground-run fallback for operators who unzipped manually.
  // MAGPIE_API_TOKEN line is conditional: with v0.2 auth on, the agent
  // must carry the token in its environment to authenticate to magpied.
  const manualCmd = isWindows
    ? `$env:MAGPIE_AGENT_NAME = $env:COMPUTERNAME
$env:MAGPIE_PRODUCT    = "${target.product}"
$env:MAGPIE_VARIANT    = "${target.variant}"
$env:MAGPIE_SERVER_URL = "${installBase.replace(/^http/, "ws")}/v1/opamp"
${tokenInUI ? `$env:MAGPIE_API_TOKEN  = "<paste-token-here>"\n` : ""}
.\\magpie-agent.exe`
    : `export MAGPIE_AGENT_NAME=$(hostname)
export MAGPIE_PRODUCT=${target.product}
export MAGPIE_VARIANT=${target.variant}
export MAGPIE_SERVER_URL=${installBase.replace(/^http/, "ws")}/v1/opamp
${tokenInUI ? "export MAGPIE_API_TOKEN=<paste-token-here>\n" : ""}
chmod +x magpie-agent otelcol-contrib
./magpie-agent`;

  async function copyOneLiner() {
    try {
      await navigator.clipboard.writeText(oneLiner);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch { /* ignore */ }
  }

  return (
    <>
      <div className="modal-scrim" onClick={onClose} aria-hidden />
      <div
        role="dialog" aria-modal="true"
        className="modal-wrap"
        onClick={onClose}
      >
        <div className="card p-6 md:p-7" style={{ maxWidth: 720, width: "100%" }} onClick={(e) => e.stopPropagation()}>
          <div className="flex items-baseline justify-between gap-4 mb-3">
            <div>
              <div className="eyebrow">Onboard a host</div>
              <h2 className="text-[22px] font-light tracking-tight" style={{ fontFamily: "var(--font-serif)" }}>
                Add to <span className="code">{target.product}</span> · <span className="code">{target.variant}</span>
              </h2>
            </div>
            <button type="button" className="btn-ghost" onClick={onClose}>Close</button>
          </div>

          {suggestedVariants.length > 1 ? (
            <div className="mb-4">
              <span className="eyebrow">Variant</span>
              <div className="seg mt-1.5 flex-wrap">
                {suggestedVariants.map((v) => (
                  <button key={v} type="button" aria-pressed={target.variant === v} onClick={() => onPickVariant(v)}>
                    {v}
                  </button>
                ))}
              </div>
            </div>
          ) : null}

          {isKubernetes ? (
            <div>
              <p className="hint" style={{ marginTop: 0 }}>
                Kubernetes hosts use the Helm chart at <span className="code">packaging/helm/magpie</span>.
                Set <span className="code">magpied.opampUrl=ws://{installBase.replace(/^https?:\/\//, "")}/v1/opamp</span> and the
                agent DaemonSet&apos;s <span className="code">product={target.product} variant=kubernetes</span>.
              </p>
            </div>
          ) : (
            <>
              {/* Primary: the one-liner. This is what 90% of operators will
                  copy-paste; the manual flow underneath is for environments
                  where curl|sudo bash isn't allowed (locked-down CI, HIPAA
                  hosts) or where the operator wants to inspect the script
                  first. */}
              <p className="hint" style={{ marginTop: 0 }}>
                {isWindows
                  ? "Run this on the target host in an elevated PowerShell:"
                  : "Run this on the target host:"}
              </p>
              <pre className="code text-[var(--color-ink-soft)] bg-[var(--color-paper-warm)] border border-[var(--color-rule)] rounded-md px-4 py-3 overflow-x-auto whitespace-pre mt-2">
                {oneLiner}
              </pre>

              <div className="mt-3 flex items-center justify-between gap-4 flex-wrap">
                <span className="text-xs text-[var(--color-muted)]" style={{ maxWidth: 460 }}>
                  {copied ? (
                    <span className="text-[var(--color-accent-ink)]">Copied.</span>
                  ) : isWindows ? (
                    <>Downloads the agent zip, extracts to <span className="code">C:\ProgramData\Magpie\bin</span>, registers the <span className="code">MagpieAgent</span> Windows Service, and starts it. Host appears here in seconds.</>
                  ) : (
                    <>Downloads the agent zip, extracts to <span className="code">/opt/magpie/bin</span>, writes a <span className="code">magpie-agent.service</span> systemd unit, and starts it. Host appears here in seconds.</>
                  )}
                </span>
                <button type="button" className="btn" onClick={copyOneLiner}>Copy</button>
              </div>

              <p className="hint" style={{ marginTop: 12 }}>
                Want to see what the script does first?{" "}
                <a className="underline" href={installURL} target="_blank" rel="noreferrer">
                  View the {isWindows ? "PowerShell" : "bash"} source
                </a>{" "}
                — it&apos;s plain text, generated per-request.
              </p>

              {/* Secondary: manual download. Collapsed by default; expanded
                  when curl|sudo bash is off-limits or the operator wants
                  more granular control. */}
              <details className="mt-5 border-t border-[var(--color-rule-soft)] pt-4">
                <summary className="eyebrow cursor-pointer select-none" style={{ marginBottom: 8 }}>
                  Manual install (download zip)
                </summary>
                <div className="mt-3">
                  {osPlatforms.length > 1 ? (
                    <div className="mb-3">
                      <span className="eyebrow">Architecture</span>
                      <div className="seg mt-1.5 flex-wrap">
                        {osPlatforms.map((p) => (
                          <button
                            key={p.arch}
                            type="button"
                            aria-pressed={arch === p.arch}
                            onClick={() => setArch(p.arch)}
                          >
                            {p.arch}
                          </button>
                        ))}
                      </div>
                    </div>
                  ) : null}

                  <div className="flex items-center justify-between gap-4 flex-wrap">
                    {chosenPlatform && targetOS ? (
                      <>
                        <span className="text-xs text-[var(--color-muted)]">
                          magpie-{targetOS}-{chosenPlatform.arch}.zip
                          {chosenPlatform.size_bytes > 0 ? ` · ${formatBytes(chosenPlatform.size_bytes)}` : ""}
                          {catalog?.version ? ` · v${catalog.version}` : ""}
                        </span>
                        <button
                          type="button"
                          className="btn-ghost"
                          onClick={async () => {
                            try {
                              const blob = await api.downloadReleaseZip(targetOS, chosenPlatform.arch);
                              saveBlob(blob, `magpie-${targetOS}-${chosenPlatform.arch}.zip`);
                            } catch (err) {
                              console.error("download zip failed", err);
                            }
                          }}
                        >
                          Download zip
                        </button>
                      </>
                    ) : (
                      <span className="text-xs text-[var(--color-muted)]">
                        {catalogError
                          ? `Release catalog unavailable: ${catalogError}`
                          : catalog === null
                            ? "Loading available builds…"
                            : `No ${targetOS ?? "build"} published yet. See docs/onboarding.md.`}
                      </span>
                    )}
                  </div>

                  <p className="hint" style={{ marginTop: 12 }}>
                    Unzip both binaries to the same directory, then run the agent in foreground:
                  </p>
                  <pre className="code text-[var(--color-ink-soft)] bg-[var(--color-paper-warm)] border border-[var(--color-rule)] rounded-md px-4 py-3 overflow-x-auto whitespace-pre mt-2 text-xs">
                    {manualCmd}
                  </pre>
                  <p className="hint" style={{ marginTop: 8 }}>
                    For a managed service install (systemd / Windows Service), use the one-liner above instead — it does both.
                  </p>
                </div>
              </details>
            </>
          )}
        </div>
      </div>
    </>
  );
}
