import {
  createRobotClient,
  GenericServiceClient,
  type RobotClient,
} from "@viamrobotics/sdk";
import { Struct } from "@bufbuild/protobuf";

export interface Credentials {
  type: "api-key";
  payload: string;
  authEntity: string;
}

export async function connect(
  host: string,
  credentials: Credentials,
): Promise<RobotClient> {
  return createRobotClient({
    host,
    credentials,
    // Required for WebRTC connections through the Viam cloud.
    signalingAddress: "https://app.viam.com:443",
  });
}

/** Thin wrapper around a generic service exposing typed DoCommand helpers. */
export class Painter {
  private svc: GenericServiceClient;

  constructor(client: RobotClient, name = "painter") {
    this.svc = new GenericServiceClient(client, name);
  }

  async do(cmd: Record<string, unknown>): Promise<Record<string, unknown>> {
    const res = await this.svc.doCommand(Struct.fromJson(cmd as never));
    return (res ?? {}) as Record<string, unknown>;
  }

  getPlane() {
    return this.do({ command: "get_plane" });
  }

  setPlane(plane: Record<string, unknown>) {
    return this.do({ command: "set_plane", ...plane });
  }

  paintPath(strokes: unknown[]) {
    return this.do({ command: "paint_path", strokes });
  }

  home() {
    return this.do({ command: "home" });
  }

  stop() {
    return this.do({ command: "stop" });
  }

  clearVisuals() {
    return this.do({ command: "clear_visuals" });
  }
}
