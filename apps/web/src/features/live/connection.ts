import { redirectToLogin } from "../../lib/api";
import { envelopeSchema, type Anomaly, type Snapshot } from "../../types/api";

export type ConnectionStatus = "connecting" | "open" | "closed";

type Handlers = {
  onSnapshot: (snapshot: Snapshot) => void;
  onAnomaly: (anomaly: Anomaly) => void;
  onStatus: (status: ConnectionStatus) => void;
};

/**
 * Reconnecting WebSocket client. After three consecutive handshake failures it
 * probes /api/auth/me so an expired proxy session redirects to login instead
 * of retrying forever.
 */
export class LiveConnection {
  private socket: WebSocket | null = null;
  private timer: number | null = null;
  private attempt = 0;
  private closed = false;

  constructor(
    private readonly projectId: string,
    private readonly handlers: Handlers,
  ) {}

  connect(): void {
    if (this.closed) return;
    this.handlers.onStatus("connecting");

    const scheme = window.location.protocol === "https:" ? "wss" : "ws";
    const socket = new WebSocket(
      `${scheme}://${window.location.host}/ws/${encodeURIComponent(this.projectId)}`,
    );
    this.socket = socket;

    socket.onopen = () => {
      this.attempt = 0;
      this.handlers.onStatus("open");
    };

    socket.onmessage = (event) => {
      let payload: unknown;
      try {
        payload = JSON.parse(String(event.data));
      } catch {
        return;
      }

      const parsed = envelopeSchema.safeParse(payload);
      if (!parsed.success) return;

      if (parsed.data.event_type === "metric_snapshot") {
        this.handlers.onSnapshot(parsed.data.data);
      } else {
        this.handlers.onAnomaly(parsed.data.data);
      }
    };

    socket.onclose = () => {
      this.handlers.onStatus("closed");
      this.scheduleReconnect();
    };

    socket.onerror = () => {
      socket.close();
    };
  }

  close(): void {
    this.closed = true;
    if (this.timer !== null) {
      window.clearTimeout(this.timer);
    }
    this.socket?.close();
  }

  private scheduleReconnect(): void {
    if (this.closed) return;

    const delay = Math.min(30_000, 1_000 * 2 ** this.attempt);
    this.attempt += 1;

    if (this.attempt === 3) {
      fetch("/api/auth/me")
        .then((response) => {
          if (response.status === 401) redirectToLogin();
        })
        .catch(() => undefined);
    }

    this.timer = window.setTimeout(() => this.connect(), delay);
  }
}
