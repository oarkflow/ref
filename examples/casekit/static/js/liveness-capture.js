// Webcam liveness capture for a pipeline `liveness` node.
//
//   import { captureLiveness } from "/assets/liveness-capture.js";
//   const outcome = await captureLiveness({
//     video: document.querySelector("video"),
//     node: "live",
//     url: (verb) => `/cases/${caseId}/stages/kyc/nodes/live/${verb}`,
//     headers: { "X-CSRF-Token": token },
//     onPrompt: (action, i, n) => show(`Step ${i + 1}/${n}: ${LABELS[action]}`),
//   });
//   // outcome.node.status is "passed", "in_progress" (try again) or "failed".
//
// It asks the server for a challenge, shows each gesture in turn while it
// samples frames from the camera, then submits the frames with the challenge
// nonce. Nothing is decided in the browser: the server scores the frames.
export const LABELS = {
  blink: "Blink",
  turn_left: "Turn your head left",
  turn_right: "Turn your head right",
  smile: "Smile",
  nod: "Nod",
  open_mouth: "Open your mouth",
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function call(url, headers, body) {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.message || data.error || `request failed (${res.status})`);
    err.status = res.status;
    err.body = data;
    throw err;
  }
  return data;
}

// The server answers with the stage view: {case, nodes: [{name, status, result}]}.
function nodeState(data, node) {
  return (data.nodes || []).find((n) => n.name === node) || {};
}

function grab(video, canvas, quality) {
  canvas.width = video.videoWidth;
  canvas.height = video.videoHeight;
  canvas.getContext("2d").drawImage(video, 0, 0);
  // Strip the data-URL prefix: the server wants bare base64.
  return canvas.toDataURL("image/jpeg", quality).split(",")[1];
}

export async function captureLiveness({
  video,
  url,
  node,
  headers = {},
  framesPerAction = 4,
  actionMs = 2500,
  quality = 0.85,
  onPrompt = () => {},
}) {
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    throw new Error("This browser cannot use the camera. Use a current browser over HTTPS.");
  }
  const issued = await call(url("challenge"), headers, {});
  const challenge = nodeState(issued, node).result;
  if (!challenge || !challenge.nonce) throw new Error("The server did not issue a challenge.");

  const stream = await navigator.mediaDevices.getUserMedia({
    video: { facingMode: "user", width: { ideal: 640 }, height: { ideal: 480 } },
    audio: false,
  });
  const frames = [];
  try {
    video.srcObject = stream;
    video.muted = true;
    await video.play();
    const canvas = document.createElement("canvas");
    const gap = actionMs / framesPerAction;
    for (let i = 0; i < challenge.actions.length; i++) {
      onPrompt(challenge.actions[i], i, challenge.actions.length);
      for (let f = 0; f < framesPerAction; f++) {
        await sleep(gap);
        frames.push(grab(video, canvas, quality));
      }
    }
  } finally {
    stream.getTracks().forEach((t) => t.stop());
    video.srcObject = null;
  }
  const done = await call(url("verify"), headers, { result: { nonce: challenge.nonce, frames } });
  return { node: nodeState(done, node), response: done };
}
