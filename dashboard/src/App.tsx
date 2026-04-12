import { useMemo, useState } from "react";
import { Navigate, NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { AgentsPage } from "./pages/AgentsPage";
import { AgentDetailPage } from "./pages/AgentDetailPage";
import { AgentTestPage } from "./pages/AgentTestPage";
import { ControlPage } from "./pages/ControlPage";
import { NotificationsPage } from "./pages/NotificationsPage";
import { SessionDetailPage } from "./pages/SessionDetailPage";
import { SessionsPage } from "./pages/SessionsPage";
import { TraceWorkspacePage } from "./pages/TraceWorkspacePage";

const tokenStorageKey = "parmesan.operator.token";

export default function App() {
  const [token, setToken] = useState<string>(() => sessionStorage.getItem(tokenStorageKey) ?? "");

  if (!token) {
    return (
      <TokenGate
        onToken={(value) => {
          sessionStorage.setItem(tokenStorageKey, value);
          setToken(value);
        }}
      />
    );
  }

  return <DashboardApp token={token} onSignOut={() => {
    sessionStorage.removeItem(tokenStorageKey);
    setToken("");
  }} />;
}

function TokenGate({ onToken }: { onToken: (token: string) => void }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  return (
    <div className="token-gate">
      <div className="token-gate__panel">
        <p className="brand-kicker">Parmesan</p>
        <h1>Operator Control Panel</h1>
        <p className="brand-copy">
          Enter an operator bearer token to inspect agents, review sessions, and submit learning feedback.
        </p>
        <form
          className="token-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!value.trim()) {
              setError("Operator token is required.");
              return;
            }
            setError("");
            onToken(value.trim());
          }}
        >
          <label>
            <span>Operator bearer token</span>
            <input
              type="password"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder="dev-operator"
            />
          </label>
          <button className="button button--primary" type="submit">
            Enter dashboard
          </button>
        </form>
        {error ? <div className="banner banner--error">{error}</div> : null}
      </div>
    </div>
  );
}

function DashboardApp({ token, onSignOut }: { token: string; onSignOut: () => void }) {
  const navItems = useMemo(
    () => [
      { to: "/sessions", label: "Sessions", detail: "Live inbox and intervention" },
      { to: "/agents", label: "Agents", detail: "Profiles, scopes, and test entry" },
      { to: "/notifications", label: "Notifications", detail: "Approvals, failures, and alerts" },
      { to: "/control", label: "Control", detail: "Policy, knowledge, and governance state" },
    ],
    [],
  );

  return (
    <div className="app-shell app-shell--nav">
      <aside className="sidebar">
        <div className="sidebar__brand">
          <p className="brand-kicker">Parmesan</p>
          <h1>Control Panel</h1>
          <p className="brand-copy">Operator console for live intervention, runtime inspection, learning feedback, and governance review.</p>
        </div>
        <nav className="nav-list">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) => `nav-list__item${isActive ? " nav-list__item--active" : ""}`}
            >
              <span>{item.label}</span>
              <small>{item.detail}</small>
            </NavLink>
          ))}
        </nav>
        <div className="sidebar__meta">
          <div>
            <span>Auth</span>
            <strong>Operator token loaded</strong>
          </div>
          <div>
            <span>Mode</span>
            <strong>Live</strong>
          </div>
          <div>
            <span>Routes</span>
            <strong>{navItems.length}</strong>
          </div>
        </div>
        <button className="button button--ghost" onClick={onSignOut} type="button">
          Sign out
        </button>
      </aside>
      <main className="workspace">
        <Routes>
          <Route path="/" element={<Navigate to="/sessions" replace />} />
          <Route path="/sessions" element={<SessionsPage token={token} />} />
          <Route path="/sessions/:sessionId" element={<SessionDetailPage token={token} />} />
          <Route path="/sessions/:sessionId/traces/:traceId" element={<TraceWorkspacePage token={token} />} />
          <Route path="/agents" element={<AgentsPage token={token} />} />
          <Route path="/agents/:agentId" element={<AgentDetailPage token={token} />} />
          <Route path="/agents/:agentId/test" element={<AgentTestPage token={token} />} />
          <Route path="/notifications" element={<NotificationsPage token={token} />} />
          <Route path="/control" element={<ControlPage token={token} />} />
          <Route path="*" element={<Navigate to="/sessions" replace />} />
        </Routes>
      </main>
    </div>
  );
}

export function useNavToSession() {
  const navigate = useNavigate();
  return (sessionID: string) => navigate(`/sessions/${sessionID}`);
}
