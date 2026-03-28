# ── EBS CSI driver ────────────────────────────────────────────────────────────
# Required for dynamic provisioning of gp3 PersistentVolumes used by ClickHouse.

resource "aws_eks_addon" "ebs_csi" {
  cluster_name                = aws_eks_cluster.main.name
  addon_name                  = "aws-ebs-csi-driver"
  resolve_conflicts_on_update = "OVERWRITE"

  depends_on = [aws_eks_node_group.workers]
}

# ── gp3 StorageClass ──────────────────────────────────────────────────────────
# gp3 delivers 3000 IOPS / 125 MB/s baseline at lower cost than gp2.
# WaitForFirstConsumer ensures PVCs are bound in the same AZ as the pod.

resource "helm_release" "gp3_storage_class" {
  name       = "gp3-storageclass"
  repository = "https://charts.helm.sh/incubator"
  chart      = "raw"
  version    = "0.2.5"
  namespace  = "kube-system"

  values = [
    <<-YAML
    resources:
      - apiVersion: storage.k8s.io/v1
        kind: StorageClass
        metadata:
          name: gp3
          annotations:
            storageclass.kubernetes.io/is-default-class: "true"
        provisioner: ebs.csi.aws.com
        volumeBindingMode: WaitForFirstConsumer
        reclaimPolicy: Retain
        parameters:
          type: gp3
          encrypted: "true"
          iops: "3000"
          throughput: "125"
    YAML
  ]

  depends_on = [
    aws_eks_addon.ebs_csi,
    aws_eks_node_group.workers,
  ]
}

# ── ClickHouse ────────────────────────────────────────────────────────────────
# Analytics data plane (Phase 3). Receives chain telemetry, reconcile audit
# records, anomaly scores, and FinOps metrics from the BatchWriter.
#
# Chart: oci://registry-1.docker.io/bitnamicharts/clickhouse

resource "helm_release" "clickhouse" {
  name             = "clickhouse"
  repository       = "oci://registry-1.docker.io/bitnamicharts"
  chart            = "clickhouse"
  version          = var.clickhouse_chart_version
  namespace        = "clickhouse"
  create_namespace = true

  set {
    name  = "shards"
    value = "1"
  }

  set {
    name  = "replicaCount"
    value = tostring(var.clickhouse_replicas)
  }

  set_sensitive {
    name  = "auth.username"
    value = "guardian"
  }

  set_sensitive {
    name  = "auth.password"
    value = var.clickhouse_admin_password
  }

  set_sensitive {
    name  = "auth.database"
    value = "taonode"
  }

  set {
    name  = "persistence.enabled"
    value = "true"
  }

  set {
    name  = "persistence.size"
    value = var.clickhouse_storage_size
  }

  set {
    name  = "persistence.storageClass"
    value = "gp3"
  }

  # Sized for m5.xlarge Spot nodes (4 vCPU / 16 GB RAM).
  set {
    name  = "resources.requests.cpu"
    value = "1"
  }

  set {
    name  = "resources.requests.memory"
    value = "4Gi"
  }

  set {
    name  = "resources.limits.cpu"
    value = "2"
  }

  set {
    name  = "resources.limits.memory"
    value = "8Gi"
  }

  # ClusterIP only — ClickHouse is an internal dependency.
  # BatchWriter reaches it at: clickhouse.clickhouse.svc.cluster.local:9000
  set {
    name  = "service.type"
    value = "ClusterIP"
  }

  set {
    name  = "podAntiAffinityPreset"
    value = "soft"
  }

  # Guard: Helm provider cannot schedule pods without an active node group.
  depends_on = [
    aws_eks_node_group.workers,
    helm_release.gp3_storage_class,
  ]
}
