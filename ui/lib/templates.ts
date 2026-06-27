// Pre-built collector config templates for common variants. Users can edit
// freely after selecting; the template just saves them from a blank textarea.

export type VariantKey = "windows" | "linux" | "kubernetes" | "custom";

export const VARIANTS: { key: VariantKey; label: string; sub: string }[] = [
  { key: "windows", label: "Windows", sub: "hostmetrics + Windows Event Log" },
  { key: "linux", label: "Linux", sub: "hostmetrics + journald" },
  { key: "kubernetes", label: "Kubernetes", sub: "k8s cluster + pod metrics" },
  { key: "custom", label: "Custom", sub: "start from a blank pipeline" },
];

export const OTLP_PLACEHOLDERS = {
  endpoint: "https://your-otlp-backend.example.com/otlp",
  token: "<INGESTION-TOKEN>",
};

function withExporter(body: string, product: string) {
  const serviceName = product && product !== "default" ? product + "-agent" : "magpie-agent";
  return body
    .replaceAll("__SERVICE_NAME__", serviceName)
    .replaceAll("__OTLP_ENDPOINT__", OTLP_PLACEHOLDERS.endpoint)
    .replaceAll("__OTLP_TOKEN__", OTLP_PLACEHOLDERS.token);
}

const WINDOWS = `receivers:
  hostmetrics:
    collection_interval: 30s
    scrapers:
      cpu:
      memory:
      load:
      disk:
      filesystem:
      network:
  windowseventlog/system:
    channel: System
  windowseventlog/application:
    channel: Application

processors:
  resourcedetection:
    detectors: [system, env]
    override: false
  resource:
    attributes:
      - key: service.name
        value: __SERVICE_NAME__
        action: upsert
  batch:
    send_batch_size: 512
    timeout: 5s

exporters:
  otlphttp/backend:
    endpoint: __OTLP_ENDPOINT__
    headers:
      authorization: __OTLP_TOKEN__

service:
  pipelines:
    metrics:
      receivers: [hostmetrics]
      processors: [resourcedetection, resource, batch]
      exporters: [otlphttp/backend]
    logs:
      receivers: [windowseventlog/system, windowseventlog/application]
      processors: [resourcedetection, resource, batch]
      exporters: [otlphttp/backend]
`;

const LINUX = `receivers:
  hostmetrics:
    collection_interval: 30s
    scrapers:
      cpu:
      memory:
      load:
      disk:
      filesystem:
      network:
  journald:
    units: [ssh, cron]

processors:
  resourcedetection:
    detectors: [system, env]
    override: false
  resource:
    attributes:
      - key: service.name
        value: __SERVICE_NAME__
        action: upsert
  batch:
    send_batch_size: 512
    timeout: 5s

exporters:
  otlphttp/backend:
    endpoint: __OTLP_ENDPOINT__
    headers:
      authorization: __OTLP_TOKEN__

service:
  pipelines:
    metrics:
      receivers: [hostmetrics]
      processors: [resourcedetection, resource, batch]
      exporters: [otlphttp/backend]
    logs:
      receivers: [journald]
      processors: [resourcedetection, resource, batch]
      exporters: [otlphttp/backend]
`;

const KUBERNETES = `receivers:
  k8s_cluster:
    collection_interval: 30s
  kubeletstats:
    collection_interval: 30s
    auth_type: serviceAccount
    endpoint: \${env:K8S_NODE_NAME}:10250
    insecure_skip_verify: true

processors:
  k8sattributes:
    auth_type: serviceAccount
    passthrough: false
    extract:
      metadata: [k8s.namespace.name, k8s.pod.name, k8s.node.name]
  resource:
    attributes:
      - key: service.name
        value: __SERVICE_NAME__
        action: upsert
  batch:
    send_batch_size: 1024
    timeout: 10s

exporters:
  otlphttp/backend:
    endpoint: __OTLP_ENDPOINT__
    headers:
      authorization: __OTLP_TOKEN__

service:
  pipelines:
    metrics:
      receivers: [k8s_cluster, kubeletstats]
      processors: [k8sattributes, resource, batch]
      exporters: [otlphttp/backend]
`;

const CUSTOM = `receivers:
  hostmetrics:
    collection_interval: 30s
    scrapers:
      cpu:
      memory:

processors:
  resource:
    attributes:
      - key: service.name
        value: __SERVICE_NAME__
        action: upsert

exporters:
  debug:
    verbosity: basic

service:
  pipelines:
    metrics:
      receivers: [hostmetrics]
      processors: [resource]
      exporters: [debug]
`;

export function templateFor(variant: VariantKey, product: string): string {
  const raw = { windows: WINDOWS, linux: LINUX, kubernetes: KUBERNETES, custom: CUSTOM }[variant];
  return withExporter(raw, product);
}
