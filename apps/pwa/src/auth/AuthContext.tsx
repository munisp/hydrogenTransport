import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { getIdentity, hasRole, initAuth, login, logout, type AuthIdentity } from "./keycloak";

interface AuthContextValue {
  identity: AuthIdentity;
  ready: boolean;
  error: Error | null;
  hasRole: (role: string) => boolean;
  login: () => void;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [identity, setIdentity] = useState<AuthIdentity>(getIdentity());
  const [ready, setReady] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  useEffect(() => {
    let cancelled = false;
    initAuth()
      .then((id) => {
        if (!cancelled) {
          setIdentity(id);
          setReady(true);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err : new Error("Authentication failed"));
          setReady(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <AuthContext.Provider value={{ identity, ready, error, hasRole, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}
