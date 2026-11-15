import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { redirectToLogin } from "../../lib/api";
import { LiveConnection } from "./connection";

vi.mock("../../lib/api", () => ({ redirectToLogin: vi.fn() }));

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  onopen: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;

  constructor(readonly url: string) {
    MockWebSocket.instances.push(this);
  }

  close(): void {
    this.onclose?.(new CloseEvent("close", { code: 1000 }));
  }

  expire(): void {
    this.onclose?.(new CloseEvent("close", {
      code: 1001,
      reason: "authentication refresh required",
      wasClean: true,
    }));
  }
}

describe("WebSocket authentication lifetime recovery", () => {
  let connection: LiveConnection;
  let fetchMock: ReturnType<typeof vi.fn>;
  const onStatus = vi.fn();

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    MockWebSocket.instances = [];
    fetchMock = vi.fn().mockResolvedValue({ status: 200 });
    vi.stubGlobal("WebSocket", MockWebSocket);
    vi.stubGlobal("fetch", fetchMock);
    connection = new LiveConnection("project/one", {
      onSnapshot: vi.fn(),
      onAnomaly: vi.fn(),
      onStatus,
    });
  });

  afterEach(() => {
    connection.close();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("opens a fresh same-origin handshake after the server's 1001 close", async () => {
    connection.connect();
    const first = MockWebSocket.instances[0];
    first.onopen?.();
    first.expire();
    expect(onStatus).toHaveBeenLastCalledWith("closed");
    await vi.advanceTimersByTimeAsync(1000);

    expect(MockWebSocket.instances).toHaveLength(2);
    const second = MockWebSocket.instances[1];
    expect(second.url).toBe(`${window.location.protocol === "https:" ? "wss" : "ws"}://${window.location.host}/ws/project%2Fone`);
    second.onopen?.();
    expect(onStatus).toHaveBeenLastCalledWith("open");
    expect(fetchMock).not.toHaveBeenCalled();
    expect(redirectToLogin).not.toHaveBeenCalled();
  });

  it("redirects to login when reconnects fail and the session probe returns 401", async () => {
    fetchMock.mockResolvedValue({ status: 401 });
    connection.connect();
    MockWebSocket.instances[0].onopen?.();
    MockWebSocket.instances[0].expire();

    await vi.advanceTimersByTimeAsync(1000);
    MockWebSocket.instances[1].close();
    await vi.advanceTimersByTimeAsync(2000);
    MockWebSocket.instances[2].close();
    await vi.advanceTimersByTimeAsync(0);

    expect(fetchMock).toHaveBeenCalledWith("/api/auth/me");
    expect(redirectToLogin).toHaveBeenCalledOnce();
  });

  it("does not reconnect if the dashboard unmounts after expiry", async () => {
    connection.connect();
    MockWebSocket.instances[0].onopen?.();
    MockWebSocket.instances[0].expire();
    connection.close();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
