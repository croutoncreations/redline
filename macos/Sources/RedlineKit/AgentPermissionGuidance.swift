public enum AgentPermissionGuidance {
    public static let title = "Agent access and macOS permissions"

    public static let summary = "When a scheduled agent or one of its tools requests protected access, macOS may show Redline as the requesting app because Redline launched the job."

    public static let detail = "The folders and capabilities depend on the job and harness configuration, so Redline does not request broad access in advance. macOS asks only when access is needed. Review the active or recent run to identify which agent was executing before allowing access."
}
