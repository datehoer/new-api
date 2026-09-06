export const meta = {
  apiVersion: 1,
  key: "xai",
  name: "xAI Grok Imagine Video",
  description: {
    en: "xAI Grok Imagine video generation via grok2api (text-to-video and image-to-video)",
    zh: "xAI Grok Imagine 视频生成（文生视频、图生视频），对接 grok2api",
  },
  version: "1.0.0",
  author: { name: "datehoer" },
  channelTypes: [48],
  models: ["grok-imagine-video", "grok-imagine-video-1.5"],
  fetchMode: "per_task",
  usageSchema: {
    seconds: {
      type: "number",
      unit: "second",
      description: {
        en: "Requested video duration in seconds. Allowed range: 1–15.",
        zh: "请求的视频时长，单位为秒。允许范围为 1–15。",
      },
    },
    resolution: {
      enum: ["480p", "720p", "1080p"],
      description: {
        en: "Requested output resolution. 1080p requires grok-imagine-video-1.5.",
        zh: "请求的输出分辨率。1080p 仅 grok-imagine-video-1.5 支持。",
      },
    },
  },
  usageExamples: [
    { label: "6s × 720p", facts: { seconds: 6, resolution: "720p" } },
    { label: "6s × 480p", facts: { seconds: 6, resolution: "480p" } },
    { label: "10s × 1080p", facts: { seconds: 10, resolution: "1080p" } },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

const ASPECT_RATIOS = ["1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"];
const RESOLUTIONS = ["480p", "720p", "1080p"];

function trimmed(value) {
  return String(value || "").trim();
}

function responsesInput(req) {
  const texts = [],
    images = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") {
          texts.push(part);
          continue;
        }
        if (!part || typeof part !== "object" || Array.isArray(part)) continue;
        if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
        if (["input_image", "image_url"].includes(part.type)) {
          let image = part.image_url;
          if (image && typeof image === "object") image = image.url;
          if (trimmed(image)) images.push(trimmed(image));
        }
      }
    }
  }
  return {
    prompt: texts
      .filter(function (text) {
        return trimmed(text);
      })
      .join("\n"),
    images: images,
  };
}

function responsesVideoText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("video artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

function mediaURL(value) {
  if (!value) return "";
  if (typeof value === "object" && !Array.isArray(value)) return trimmed(value.url || value.image_url);
  return trimmed(value);
}

function isVideo15(model) {
  return trimmed(model).indexOf("grok-imagine-video-1.5") >= 0;
}

function closestAspect(width, height) {
  if (!(width > 0 && height > 0)) return "16:9";
  const ratio = width / height;
  let best = "16:9";
  let bestDelta = Infinity;
  const candidates = { "1:1": 1, "16:9": 16 / 9, "9:16": 9 / 16, "4:3": 4 / 3, "3:4": 3 / 4, "3:2": 3 / 2, "2:3": 2 / 3 };
  for (const name of ASPECT_RATIOS) {
    const delta = Math.abs(ratio - candidates[name]);
    if (delta < bestDelta) {
      best = name;
      bestDelta = delta;
    }
  }
  return best;
}

function parseSize(size) {
  const lower = trimmed(size).toLowerCase();
  if (!lower) return null;
  if (ASPECT_RATIOS.indexOf(lower) >= 0) return { aspect: lower, resolution: "" };
  if (RESOLUTIONS.indexOf(lower) >= 0) return { aspect: "", resolution: lower };
  const parts = lower.split("x");
  if (parts.length !== 2) return null;
  const width = Number(parts[0]);
  const height = Number(parts[1]);
  if (!(width > 0 && height > 0)) return null;
  const max = Math.max(width, height);
  let resolution = "720p";
  if (max >= 1920) resolution = "1080p";
  else if (max >= 1280) resolution = "720p";
  else if (max >= 640) resolution = "480p";
  return { aspect: closestAspect(width, height), resolution: resolution };
}

function durationSeconds(req) {
  const raw = req.duration !== undefined ? req.duration : req.seconds;
  if (raw === undefined || raw === null || raw === "") return 8;
  const value = Math.round(Number(raw));
  if (!Number.isFinite(value) || value < 1 || value > 15) throw new Error("duration must be between 1 and 15 seconds");
  return value;
}

function collectImages(req) {
  const images = [];
  const push = function (value) {
    const url = mediaURL(value);
    if (url && images.indexOf(url) < 0) images.push(url);
  };
  push(req.image);
  push(req.image_url);
  push(req.input_reference);
  if (Array.isArray(req.images)) {
    for (const item of req.images) push(item);
  }
  if (Array.isArray(req.reference_images)) {
    for (const item of req.reference_images) push(item);
  }
  return images;
}

function outbound(req, model) {
  const metadata = req.metadata && typeof req.metadata === "object" && !Array.isArray(req.metadata) ? req.metadata : {};
  const duration = durationSeconds(req);
  let aspect = trimmed(req.aspect_ratio || metadata.aspect_ratio).toLowerCase();
  let resolution = trimmed(req.resolution || metadata.resolution).toLowerCase();
  const parsed = parseSize(req.size || metadata.size);
  if (parsed) {
    if (!aspect) aspect = parsed.aspect;
    if (!resolution) resolution = parsed.resolution;
  }
  if (!aspect) aspect = "16:9";
  if (!resolution) resolution = "720p";
  if (ASPECT_RATIOS.indexOf(aspect) < 0) throw new Error("aspect_ratio must be one of " + ASPECT_RATIOS.join(", "));
  if (RESOLUTIONS.indexOf(resolution) < 0) throw new Error("resolution must be one of " + RESOLUTIONS.join(", "));
  if (resolution === "1080p" && !isVideo15(model)) throw new Error("1080p requires grok-imagine-video-1.5");

  const images = collectImages(req);
  const explicitRefs = Array.isArray(req.reference_images) && req.reference_images.length > 0;
  const firstFrame = mediaURL(req.image) || mediaURL(req.image_url) || mediaURL(req.input_reference);
  if (explicitRefs && firstFrame) throw new Error("image cannot be used together with reference_images");
  const body = {
    model: trimmed(model || req.model),
    prompt: trimmed(req.prompt),
    duration: duration,
    aspect_ratio: aspect,
    resolution: resolution,
  };
  if (explicitRefs || images.length > 1) {
    if (images.length > 7) throw new Error("reference_images accepts at most 7 images");
    body.reference_images = images.map(function (url) {
      return { url: url };
    });
  } else if (images.length === 1) {
    body.image = { url: images[0] };
  }
  if (!body.prompt && !body.image && !body.reference_images) throw new Error("prompt is required");
  return { body: body, action: body.image || body.reference_images ? "image_to_video" : "text_to_video" };
}

export function buildSubmitRequest(ctx) {
  const result = outbound(ctx.requestBody || {}, ctx.upstreamModel || ctx.model);
  return {
    url: ctx.baseUrl + "/v1/videos/generations",
    method: "POST",
    headers: { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json" },
    body: result.body,
    action: result.action,
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  const taskId = trimmed(body.request_id || body.id || body.task_id);
  if (!taskId) throw new Error("request_id is empty");
  return { taskId: taskId, taskData: body };
}

export function extractUsage(ctx) {
  const req = ctx.requestBody || {};
  const result = outbound(req, ctx.upstreamModel || ctx.model || req.model);
  return { seconds: result.body.duration, resolution: result.body.resolution };
}

export function extractUsageOnComplete(task, taskResult, body) {
  const payload = body && typeof body === "object" ? body : {};
  const seconds = Number((payload.video && payload.video.duration) || payload.duration || 0);
  if (!Number.isFinite(seconds) || seconds <= 0) return {};
  return { seconds: Math.min(seconds, 15) };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  const payload = body && typeof body === "object" ? body : {};
  const status = trimmed(payload.status).toLowerCase();
  const errorMessage = payload.error && payload.error.message ? String(payload.error.message) : "";
  if (status === "failed" || status === "error" || status === "cancelled") {
    return { status: "FAILURE", progress: "100%", reason: errorMessage || "video generation failed" };
  }
  if (status === "done" || status === "completed" || status === "success") {
    const url = mediaURL(payload.video);
    return { status: "SUCCESS", progress: "100%", url: url, remoteUrl: url };
  }
  if (!status && errorMessage) {
    return { status: "FAILURE", progress: "100%", reason: errorMessage };
  }
  const progress = Number(payload.progress);
  const pct = Number.isFinite(progress) ? Math.max(0, Math.min(99, Math.round(progress))) : 0;
  return { status: pct > 0 ? "IN_PROGRESS" : "QUEUED", progress: pct + "%" };
}

function pollVideoURL(task) {
  const data = (task && task.data) || {};
  return mediaURL(data.video);
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const url = pollVideoURL(ctx);
  if (url) {
    return { url: url, method: ctx.clientRequest.method, credentialless: true };
  }
  const taskId = trimmed(ctx.upstreamTaskId || ctx.taskId);
  if (!taskId) throw new Error("artifact_not_found");
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(taskId) + "/content",
    method: ctx.clientRequest.method,
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

function decodeVideoRequest(ctx) {
  if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
  let req;
  if (ctx.body.kind === "json") {
    if (!ctx.body.value || typeof ctx.body.value !== "object" || Array.isArray(ctx.body.value)) throw new Error("JSON object required");
    req = Object.assign({}, ctx.body.value);
  } else {
    const first = function (name) {
      const values = (ctx.body.fields || {})[name] || [];
      if (values.length > 1) throw new Error(name + " must be provided once");
      return values[0];
    };
    req = {};
    const fields = ctx.body.fields || {};
    for (const name of Object.keys(fields)) req[name] = first(name);
    if (req.metadata !== undefined) {
      let parsed;
      try {
        parsed = JSON.parse(req.metadata);
      } catch (e) {
        throw new Error("metadata must be a JSON object string");
      }
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("metadata must be a JSON object string");
      req.metadata = parsed;
    }
    if (req.seconds !== undefined) req.seconds = Number(req.seconds);
    else if (req.duration !== undefined) req.duration = Number(req.duration);
    for (const file of ctx.body.files || []) {
      if (file.field !== "input_reference") throw new Error("unexpected file field: " + file.field);
      throw new Error("xAI video currently accepts image URLs, not uploaded files");
    }
  }
  const model = trimmed(ctx.model || req.model);
  if (!model) throw new Error("model is required");
  const input = responsesInput(req);
  if (!trimmed(req.prompt) && input.prompt) req.prompt = input.prompt;
  if (input.images.length) {
    req.images = (req.images || []).concat(input.images);
  }
  const result = outbound(req, model);
  return { kind: "submit", model: model, action: result.action, requestBody: result.body };
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata)))
        throw new Error("metadata must be an object");
      return decodeVideoRequest(ctx);
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesVideoText(ctx);
        const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE")
        return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
      if (previousState && previousState.status === status && previousState.progress === progress) return { events: [], state: state, done: false };
      const event = { type: "progress", message: status.toLowerCase() };
      if (progress !== null) event.progress = progress;
      return { events: [event], state: state, done: false };
    },
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "xai" },
      };
    },
  },
};

const legacyRenderers = {
  openai_video: function (task) {
    const statuses = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
    const output = {
      id: task.task_id,
      object: "video",
      model: (task.properties || {}).origin_model_name || "",
      status: statuses[task.status] || "unknown",
      progress: Number(String(task.progress || "0").replace("%", "")),
      created_at: Number(task.created_at || 0),
    };
    const completedAt = Number(task.finished_at || task.updated_at || 0);
    if (completedAt > 0) output.completed_at = completedAt;
    if (task.status === "FAILURE") {
      output.error = { message: task.fail_reason || "The video generation task failed.", code: "video_generation_failed" };
    }
    return output;
  },
};

protocols.openai_video = {
  decodeRequest: decodeVideoRequest,
  render: function (ctx, task) {
    return legacyRenderers.openai_video(task);
  },
};
