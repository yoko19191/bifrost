import { isRegexEntry, validatePatternAlias } from "@/components/modelAccess/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { AliasConfig, EffortModels, ModelFamily, ModelFamilyValues } from "@/lib/types/config";
import { useGetModelsQuery } from "@/lib/store";
import { hasEffortModelsConfigured, isLiteralHTTPSURL, SecretVar } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { parseVertexEndpointURL } from "@/lib/vertexEndpoint";
import { ChevronDown, ChevronRight, Layers, Regex, Sparkles, Trash, Wand2 } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

type DeploymentsValue = Record<string, AliasConfig> | undefined | null;

interface Props {
	value: DeploymentsValue;
	onChange: (next: Record<string, AliasConfig>) => void;
	providerName: string;
	availableModels?: string[];
	onCleanSourceModels?: (sourceIdsToRemove: string[], fusedBaseIdsToAdd: string[]) => void;
	disabled?: boolean;
}

interface Row {
	name: string;
	config: AliasConfig;
}

// Normalize legacy shapes (Record<string, string> from older configs or stringified JSON)
// into the rich Record<string, AliasConfig> the component operates on.
function normalize(value: DeploymentsValue): Record<string, AliasConfig> {
	if (value == null) {
		return {};
	}
	if (typeof value === "string") {
		try {
			const parsed = JSON.parse(value);
			return normalize(parsed);
		} catch {
			return {};
		}
	}
	if (typeof value !== "object" || Array.isArray(value)) {
		return {};
	}
	const out: Record<string, AliasConfig> = {};
	for (const [k, v] of Object.entries(value)) {
		if (typeof v === "string") {
			out[k] = { model_id: v };
		} else if (v && typeof v === "object") {
			const cfg = v as Partial<AliasConfig>;
			out[k] = { ...cfg, model_id: typeof cfg.model_id === "string" ? cfg.model_id : "" };
		}
	}
	return out;
}

const emptySecretVar: SecretVar = { value: "", ref: "" };
const isEmptySecretVar = (v: SecretVar | undefined): boolean => !v || (!v.value && !v.ref);

function PatternMark({ testId }: { testId?: string }) {
	const { t } = useTranslation("models");
	const label = t("providers.deployments.patternBadge");
	return (
		<span
			className="border-input text-muted-foreground absolute inset-y-0 right-0 flex w-9 items-center justify-center border-l"
			title={label}
			aria-label={label}
			role="img"
			data-testid={testId}
		>
			<Regex className="h-4 w-4" aria-hidden />
		</span>
	);
}

function FieldRow({ label, hint, htmlFor, children }: { label: string; hint?: string; htmlFor?: string; children: React.ReactNode }) {
	return (
		<div className="space-y-1.5">
			<label htmlFor={htmlFor} className="text-sm font-medium">
				{label}
			</label>
			{children}
			{hint && <p className="text-muted-foreground text-xs">{hint}</p>}
		</div>
	);
}

function SectionHeader({ title, description }: { title: string; description?: string }) {
	return (
		<div className="border-b pb-2">
			<h4 className="text-sm font-semibold">{title}</h4>
			{description && <p className="text-muted-foreground mt-0.5 text-xs">{description}</p>}
		</div>
	);
}

function SecretVarField({
	value,
	onChange,
	placeholder,
	disabled,
}: {
	value: SecretVar | undefined;
	onChange: (next: SecretVar | undefined) => void;
	placeholder?: string;
	disabled?: boolean;
}) {
	return (
		<SecretVarInput
			value={value ?? emptySecretVar}
			onChange={(next) => onChange(isEmptySecretVar(next) ? undefined : next)}
			placeholder={placeholder}
			disabled={disabled}
		/>
	);
}

function StringField({
	value,
	onChange,
	placeholder,
	disabled,
}: {
	value: string | undefined;
	onChange: (next: string | undefined) => void;
	placeholder?: string;
	disabled?: boolean;
}) {
	return (
		<Input
			value={value ?? ""}
			onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
			placeholder={placeholder}
			disabled={disabled}
		/>
	);
}

interface ProviderSectionProps {
	config: AliasConfig;
	onChange: (patch: Partial<AliasConfig>) => void;
	disabled?: boolean;
}

// Three-way control for boolean overrides that inherit a key-level toggle when
// unset: undefined = use the key's setting, true/false = explicit override. A
// plain switch can't express "explicitly off while the key-level toggle is on".
function TriStateOverrideRow({
	label,
	hint,
	value,
	onChange,
	disabled,
	testId,
}: {
	label: string;
	hint: string;
	value: boolean | undefined;
	onChange: (next: boolean | undefined) => void;
	disabled?: boolean;
	testId?: string;
}) {
	const { t } = useTranslation("models");
	const id = useId();
	const hintId = `${id}-hint`;
	const selectValue = value === undefined ? "inherit" : value ? "on" : "off";
	return (
		<div className="flex items-start justify-between gap-4 rounded-md border p-3">
			<div className="space-y-0.5">
				<label htmlFor={id} className="text-sm font-medium">
					{label}
				</label>
				<p id={hintId} className="text-muted-foreground text-xs">
					{hint}
				</p>
			</div>
			<Select value={selectValue} onValueChange={(v) => onChange(v === "inherit" ? undefined : v === "on")} disabled={disabled}>
				<SelectTrigger id={id} aria-describedby={hintId} className="w-fit min-w-44 shrink-0" data-testid={testId}>
					<SelectValue />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="inherit">{t("providers.deployments.useKeySetting")}</SelectItem>
					<SelectItem value="on">{t("providers.deployments.on")}</SelectItem>
					<SelectItem value="off">{t("providers.deployments.off")}</SelectItem>
				</SelectContent>
			</Select>
		</div>
	);
}

function AzureSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.azureOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Azure" })}
			/>
			<FieldRow label={t("providers.deployments.apiVersion")} hint={t("providers.deployments.apiVersionHint")}>
				<StringField
					value={config.api_version}
					onChange={(v) => onChange({ api_version: v })}
					placeholder="2024-10-21"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.anthropicVersion")} hint={t("providers.deployments.anthropicVersionHint")}>
				<StringField
					value={config.anthropic_version}
					onChange={(v) => onChange({ anthropic_version: v })}
					placeholder="2023-06-01"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.endpoint")} hint={t("providers.deployments.endpointHint")}>
				<SecretVarField
					value={config.endpoint}
					onChange={(v) => onChange({ endpoint: v })}
					placeholder={t("providers.deployments.azureEndpointPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<TriStateOverrideRow
				label={t("providers.deployments.azureUseSpeech")}
				hint={t("providers.deployments.azureUseSpeechHint")}
				value={config.use_speech_endpoints}
				onChange={(next) => onChange({ use_speech_endpoints: next })}
				disabled={disabled}
				testId="deployment-azure-use-speech-endpoints"
			/>
			<TriStateOverrideRow
				label={t("providers.deployments.azureUseVoiceLive")}
				hint={t("providers.deployments.azureUseVoiceLiveHint")}
				value={config.use_voice_live_endpoints}
				onChange={(next) => onChange({ use_voice_live_endpoints: next })}
				disabled={disabled}
				testId="deployment-azure-use-voice-live-endpoints"
			/>
		</div>
	);
}

function VertexSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	const endpoint = config.vertex_endpoint;
	const servingId = useId();
	const endpointId = useId();
	const endpointInvalid = !!endpoint && !/^[A-Za-z0-9_-]+$/.test(endpoint.endpoint_id);
	return (
		<div className="space-y-4">
			<FieldRow htmlFor={servingId} label={t("providers.deployments.servingMode")} hint={t("providers.deployments.servingModeHint")}>
				<Select
					value={endpoint ? (endpoint.method ?? "raw_predict") : "standard"}
					onValueChange={(value) =>
						onChange({
							vertex_endpoint:
								value === "standard"
									? undefined
									: {
											endpoint_id: endpoint?.endpoint_id ?? "",
											format: "openai_chat",
											method: value as "raw_predict" | "chat_completions",
										},
						})
					}
					disabled={disabled}
				>
					<SelectTrigger id={servingId} className="w-full" data-testid="deployment-vertex-serving-mode">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="standard">{t("providers.deployments.servingStandard")}</SelectItem>
						<SelectItem value="raw_predict">{t("providers.deployments.servingRawPredict")}</SelectItem>
						<SelectItem value="chat_completions">{t("providers.deployments.servingChatCompletions")}</SelectItem>
					</SelectContent>
				</Select>
			</FieldRow>
			{endpoint && (
				<FieldRow
					htmlFor={endpointId}
					label={t("providers.deployments.vertexEndpoint")}
					hint={t("providers.deployments.vertexEndpointHint")}
				>
					<Input
						id={endpointId}
						aria-invalid={endpointInvalid}
						aria-describedby={endpointInvalid ? `${endpointId}-error` : undefined}
						value={endpoint.endpoint_id}
						onChange={(event) => {
							const value = event.target.value;
							const parsed = parseVertexEndpointURL(value);
							if (parsed) {
								onChange({
									vertex_endpoint: { ...parsed.endpoint, format: endpoint.format },
									project_id: { value: parsed.projectId, ref: "" },
									region: { value: parsed.region, ref: "" },
								});
							} else {
								onChange({ vertex_endpoint: { ...endpoint, endpoint_id: value } });
							}
						}}
						placeholder={t("providers.deployments.vertexEndpointPlaceholder")}
						disabled={disabled}
						data-testid="deployment-vertex-endpoint"
					/>
					{endpointInvalid && (
						<p id={`${endpointId}-error`} className="text-destructive text-xs" data-testid="deployment-vertex-endpoint-error">
							{t("providers.deployments.vertexEndpointInvalid")}
						</p>
					)}
				</FieldRow>
			)}
			<SectionHeader
				title={t("providers.deployments.vertexOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Vertex" })}
			/>
			<FieldRow label={t("providers.deployments.projectId")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder={t("providers.deployments.vertexProjectIdPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			{!endpoint && (
				<FieldRow label={t("providers.deployments.projectNumber")} hint={t("providers.deployments.projectNumberHint")}>
					<SecretVarField
						value={config.project_number}
						onChange={(v) => onChange({ project_number: v })}
						placeholder={t("providers.deployments.vertexProjectNumberPlaceholder")}
						disabled={disabled}
					/>
				</FieldRow>
			)}
			<FieldRow
				label={t("providers.deployments.region")}
				hint={endpoint ? t("providers.deployments.regionHintEndpoint") : t("providers.deployments.regionHint")}
			>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder={t("providers.deployments.vertexRegionPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			{!endpoint && (
				<div className="flex items-start justify-between gap-4 rounded-md border p-3">
					<div className="space-y-0.5">
						<label htmlFor="force-single-region" className="text-sm font-medium">
							{t("providers.keys.forceSingleRegion")}
						</label>
						<p className="text-muted-foreground text-xs">{t("providers.deployments.forceSingleRegionHint")}</p>
					</div>
					<Switch
						id="force-single-region"
						checked={config.force_single_region ?? false}
						onCheckedChange={(checked) => onChange({ force_single_region: checked })}
						disabled={disabled}
					/>
				</div>
			)}
		</div>
	);
}

function BedrockSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.bedrockOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Bedrock" })}
			/>
			<FieldRow label={t("providers.deployments.region")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder={t("providers.deployments.bedrockRegionPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.inferenceProfileArn")} hint={t("providers.deployments.inferenceProfileArnHint")}>
				<SecretVarField
					value={config.inference_profile_arn}
					onChange={(v) => onChange({ inference_profile_arn: v })}
					placeholder={t("providers.deployments.profileArnPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.projectId")} hint={t("providers.deployments.bedrockProjectHint")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder={t("providers.deployments.projectIdPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function BedrockMantleSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.bedrockMantleOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Bedrock Mantle" })}
			/>
			<FieldRow label={t("providers.deployments.region")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder={t("providers.deployments.bedrockRegionPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.projectId")} hint={t("providers.deployments.mantleProjectHint")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder={t("providers.deployments.projectIdPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function ReplicateSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.replicateOverrides")}
				description={t("providers.deployments.overrideDefaultsShort", { provider: "Replicate" })}
			/>
			<TriStateOverrideRow
				label={t("providers.deployments.useDeploymentsEndpoint")}
				hint={t("providers.deployments.useDeploymentsEndpointHint")}
				value={config.use_deployments_endpoint}
				onChange={(next) => onChange({ use_deployments_endpoint: next })}
				disabled={disabled}
				testId="deployment-use-deployments-endpoint"
			/>
		</div>
	);
}

function BasetenSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	const { t: tCommon } = useTranslation("common");
	const inputId = useId();
	const errorId = `${inputId}-error`;
	const invalid = !isLiteralHTTPSURL(config.deployment_url);
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.providerOverrides", { provider: "Baseten" })}
				description={t("providers.deployments.overrideDefaultsShort", { provider: "Baseten" })}
			/>
			<FieldRow htmlFor={inputId} label={t("providers.deployments.deploymentUrl")} hint={t("providers.deployments.deploymentUrlHint")}>
				<SecretVarInput
					id={inputId}
					value={config.deployment_url ?? emptySecretVar}
					onChange={(next) => onChange({ deployment_url: isEmptySecretVar(next) ? undefined : next })}
					placeholder={t("providers.deployments.deploymentUrlPlaceholder")}
					disabled={disabled}
					aria-invalid={invalid}
					aria-describedby={invalid ? errorId : undefined}
					data-testid="deployment-baseten-deployment-url"
				/>
				{invalid && (
					<p id={errorId} className="text-destructive text-xs" data-testid="deployment-baseten-deployment-url-error">
						{tCommon("validation.basetenDeploymentUrl")}
					</p>
				)}
			</FieldRow>
		</div>
	);
}

function UseAnthropicEndpointsToggleSection({ config, onChange, disabled, providerName }: ProviderSectionProps & { providerName: string }) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.providerOverrides", { provider: providerName })}
				description={t("providers.deployments.overrideDefaultsShort", { provider: providerName })}
			/>
			<TriStateOverrideRow
				label={t("providers.deployments.useAnthropicEndpoints")}
				hint={t("providers.deployments.useAnthropicEndpointsHint")}
				value={config.use_anthropic_endpoints}
				onChange={(next) => onChange({ use_anthropic_endpoints: next })}
				disabled={disabled}
				testId="deployment-use-anthropic-endpoints"
			/>
		</div>
	);
}

function ProviderSection({ providerName, ...props }: ProviderSectionProps & { providerName: string }) {
	switch (providerName) {
		case "azure":
			return <AzureSection {...props} />;
		case "vertex":
			return <VertexSection {...props} />;
		case "bedrock":
			return <BedrockSection {...props} />;
		case "bedrock_mantle":
			return <BedrockMantleSection {...props} />;
		case "replicate":
			return <ReplicateSection {...props} />;
		case "baseten":
			return <BasetenSection {...props} />;
		case "sgl":
			return <UseAnthropicEndpointsToggleSection providerName="SGLang" {...props} />;
		case "deepseek":
			return <UseAnthropicEndpointsToggleSection providerName="Deepseek" {...props} />;
		case "fireworks":
			return <UseAnthropicEndpointsToggleSection providerName="Fireworks" {...props} />;
		case "vllm":
			return <UseAnthropicEndpointsToggleSection providerName="vLLM" {...props} />;
		default:
			return null;
	}
}

const EFFORT_SUFFIXES = ["-minimal", "-low", "-medium", "-high", "-xhigh", "-max"] as const;

function cleanEffortModels(next: EffortModels): EffortModels | undefined {
	const cleaned: EffortModels = {};
	if (next.low?.trim()) cleaned.low = next.low.trim();
	if (next.medium?.trim()) cleaned.medium = next.medium.trim();
	if (next.high?.trim()) cleaned.high = next.high.trim();
	if (next.default?.trim()) cleaned.default = next.default.trim();
	return hasEffortModelsConfigured(cleaned) ? cleaned : undefined;
}

export interface DetectedEffortFamily {
	baseId: string;
	modelId: string;
	effortModels: EffortModels;
	sourceIds: string[];
	kind: "tiered" | "thinking" | "sibling" | "preset";
}

const TIER_SUFFIX_RE = /^(.+)-(low|medium|high)$/i;
const THINKING_SUFFIX_RE = /^(.+)-thinking$/i;

export function detectSmartEffortFamilies(modelIds: readonly string[], providerName?: string): DetectedEffortFamily[] {
	const cleaned: string[] = [];
	const exactByLower = new Map<string, string>();
	for (const raw of modelIds) {
		let id = (raw ?? "").trim();
		if (!id || id === "*" || id.startsWith("regex:")) continue;
		const slashIdx = id.indexOf("/");
		if (slashIdx > 0 && slashIdx < id.length - 1) {
			const prefix = id.slice(0, slashIdx).toLowerCase();
			if (prefix === "antigravity" || prefix === "codex" || prefix === "claude" || prefix === "grok") {
				id = id.slice(slashIdx + 1);
			}
		}
		const lower = id.toLowerCase();
		if (!exactByLower.has(lower)) {
			exactByLower.set(lower, id);
			cleaned.push(id);
		}
	}

	const families = new Map<
		string,
		{
			baseId: string;
			low?: string;
			medium?: string;
			high?: string;
			thinking?: string;
			sourceIds: Set<string>;
		}
	>();

	const getOrCreate = (baseId: string) => {
		const lower = baseId.toLowerCase();
		let entry = families.get(lower);
		if (!entry) {
			entry = { baseId, sourceIds: new Set<string>() };
			families.set(lower, entry);
		}
		return entry;
	};

	for (const id of cleaned) {
		const tierMatch = TIER_SUFFIX_RE.exec(id);
		if (tierMatch) {
			const baseId = tierMatch[1]!;
			const tier = tierMatch[2]!.toLowerCase() as "low" | "medium" | "high";
			const entry = getOrCreate(baseId);
			entry[tier] = id;
			entry.sourceIds.add(id);
			const tieredVariant = exactByLower.get(`${baseId.toLowerCase()}-tiered`);
			if (tieredVariant) {
				entry.sourceIds.add(tieredVariant);
			}
			continue;
		}
		const thinkingMatch = THINKING_SUFFIX_RE.exec(id);
		if (thinkingMatch) {
			const baseId = thinkingMatch[1]!;
			const entry = getOrCreate(baseId);
			entry.thinking = id;
			entry.sourceIds.add(id);
		}
	}

	const firstNonEmpty = (...vals: (string | undefined)[]): string => {
		for (const v of vals) {
			if (v && v.trim()) return v.trim();
		}
		return "";
	};

	const results: DetectedEffortFamily[] = [];
	for (const [, entry] of families.entries()) {
		const standaloneBase = exactByLower.get(entry.baseId.toLowerCase());
		const tierCount = [entry.low, entry.medium, entry.high].filter(Boolean).length;
		if (tierCount === 0 && !entry.thinking) continue;
		if (tierCount === 1 && !entry.thinking && standaloneBase) continue;

		const low = firstNonEmpty(entry.low, standaloneBase, entry.medium, entry.high, entry.thinking);
		const medium = firstNonEmpty(entry.medium, entry.thinking, entry.high, entry.low, standaloneBase);
		const high = firstNonEmpty(entry.high, entry.thinking, entry.medium, entry.low, standaloneBase);
		const defaultTier = entry.medium || entry.thinking ? "medium" : entry.high ? "high" : "low";
		const defaultModel = defaultTier === "medium" ? medium : defaultTier === "high" ? high : low;
		if (!defaultModel) continue;

		const canonicalBase = standaloneBase ?? entry.baseId;
		results.push({
			baseId: canonicalBase,
			modelId: defaultModel,
			effortModels: {
				low,
				medium,
				high,
				default: defaultTier,
			},
			sourceIds: [...entry.sourceIds],
			kind: tierCount >= 2 ? "tiered" : entry.thinking ? "thinking" : "tiered",
		});
	}

	results.sort((a, b) => a.baseId.toLowerCase().localeCompare(b.baseId.toLowerCase()));
	if (results.length > 0) {
		return results;
	}

	// Fallback when no tier/thinking suffixes exist in the model list
	const p = (providerName ?? "").toLowerCase().trim();
	if (p === "codex" || p === "openai") {
		const codexBase =
			exactByLower.get("gpt-5.3-codex") ??
			cleaned.find((m) => m.toLowerCase().includes("codex")) ??
			cleaned.find((m) => m.toLowerCase().startsWith("gpt-5")) ??
			"gpt-5.3-codex";
		return [
			{
				baseId: codexBase,
				modelId: codexBase,
				effortModels: { low: codexBase, medium: codexBase, high: codexBase, default: "medium" },
				sourceIds: [],
				kind: "preset",
			},
		];
	}
	if (p === "claude" || p === "anthropic") {
		const opus = exactByLower.get("claude-opus-4-6") ?? "claude-opus-4-6";
		const sonnet = exactByLower.get("claude-sonnet-4-6") ?? opus;
		return [
			{
				baseId: opus,
				modelId: opus,
				effortModels: { low: sonnet, medium: opus, high: opus, default: "medium" },
				sourceIds: [],
				kind: "sibling",
			},
		];
	}
	if (p === "grok" || p === "xai") {
		const grokBase = exactByLower.get("grok-4") ?? cleaned.find((m) => m.toLowerCase().startsWith("grok-4")) ?? "grok-4";
		const grokLow = exactByLower.get("grok-3-mini") ?? grokBase;
		const grokHigh = exactByLower.get("grok-4-heavy") ?? grokBase;
		return [
			{
				baseId: grokBase,
				modelId: grokBase,
				effortModels: { low: grokLow, medium: grokBase, high: grokHigh, default: "medium" },
				sourceIds: [],
				kind: "sibling",
			},
		];
	}
	return [];
}

function EffortFusionSection({
	aliasName,
	config,
	onChange,
	providerName,
	disabled,
}: {
	aliasName?: string;
	config: AliasConfig;
	onChange: (patch: Partial<AliasConfig>) => void;
	providerName: string;
	disabled?: boolean;
}) {
	const effort = config.effort_models ?? {};
	const hasEffort = hasEffortModelsConfigured(effort);
	const displayAlias = aliasName?.trim() || "model-alias";

	const updateTier = (tier: keyof EffortModels, val: string) => {
		const nextEffort = cleanEffortModels({ ...effort, [tier]: val });
		const patch: Partial<AliasConfig> = { effort_models: nextEffort };
		if (!config.model_id?.trim() && val.trim()) {
			patch.model_id = val.trim();
		}
		onChange(patch);
	};

	const fillFromMainModel = () => {
		const base = config.model_id?.trim() || effort.default || effort.high || effort.medium || effort.low || "";
		if (!base) return;
		onChange({
			model_id: base,
			effort_models: {
				low: base,
				medium: base,
				high: base,
				default: base,
			},
		});
	};

	return (
		<div className="bg-muted/20 space-y-3.5 rounded-lg border p-3.5">
			<div className="flex flex-wrap items-start justify-between gap-2 border-b pb-2.5">
				<div className="space-y-0.5">
					<div className="flex items-center gap-2">
						<Layers className="text-primary h-4 w-4" />
						<h4 className="text-sm font-semibold">Effort 双向融合路由 (Mode A 档位聚合 + Mode B 虚拟后缀)</h4>
						{hasEffort && (
							<Badge variant="default" className="text-[10px]">
								已激活融合
							</Badge>
						)}
					</div>
					<p className="text-muted-foreground text-xs">
						Mode A：根据请求的 <code>reasoning.effort</code> 或 <code>reasoning.max_tokens</code> 自动路由至对应档位上游模型；Mode B：直接请求{" "}
						<code>{displayAlias}-low/medium/high/max</code> 时自动剥离后缀并注入对应推理强度。
					</p>
				</div>
				<div className="flex items-center gap-1.5">
					{config.model_id?.trim() && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="h-7 gap-1 text-xs"
							disabled={disabled}
							onClick={fillFromMainModel}
						>
							<Wand2 className="h-3 w-3" />
							用主模型填充三档
						</Button>
					)}
					{hasEffort && (
						<Button
							type="button"
							variant="ghost"
							size="sm"
							className="text-muted-foreground hover:text-destructive h-7 text-xs"
							disabled={disabled}
							onClick={() => onChange({ effort_models: undefined })}
						>
							清空档位
						</Button>
					)}
				</div>
			</div>

			<div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
				<FieldRow
					label="Low 档位模型 (low / minimal)"
					hint="当 effort 为 low/minimal 或思维预算 <= 4096 tokens 时路由"
				>
					<ModelSelector
						provider={providerName}
						value={effort.low ?? ""}
						onChange={(v) => updateTier("low", v)}
						placeholder="选择或输入 Low 档位上游模型 ID"
						disabled={disabled}
						unfiltered
						allowCustomModel
						data-testid={`deployment-effort-low-${displayAlias}`}
					/>
				</FieldRow>
				<FieldRow
					label="Medium 档位模型 (medium)"
					hint="当 effort 为 medium 或思维预算 <= 16384 tokens 时路由"
				>
					<ModelSelector
						provider={providerName}
						value={effort.medium ?? ""}
						onChange={(v) => updateTier("medium", v)}
						placeholder="选择或输入 Medium 档位上游模型 ID"
						disabled={disabled}
						unfiltered
						allowCustomModel
						data-testid={`deployment-effort-medium-${displayAlias}`}
					/>
				</FieldRow>
				<FieldRow
					label="High 档位模型 (high / xhigh / max)"
					hint="当 effort 为 high/xhigh/max 或大预算深度思考时路由"
				>
					<ModelSelector
						provider={providerName}
						value={effort.high ?? ""}
						onChange={(v) => updateTier("high", v)}
						placeholder="选择或输入 High 档位上游模型 ID"
						disabled={disabled}
						unfiltered
						allowCustomModel
						data-testid={`deployment-effort-high-${displayAlias}`}
					/>
				</FieldRow>
				<FieldRow
					label="Default 默认回退模型 (可选)"
					hint="未显式指定 effort 时的回退模型（留空则使用主 Model ID 或 High -> Medium -> Low）"
				>
					<ModelSelector
						provider={providerName}
						value={effort.default ?? ""}
						onChange={(v) => updateTier("default", v)}
						placeholder={config.model_id || "留空则回退至主 Model ID"}
						disabled={disabled}
						unfiltered
						allowCustomModel
						data-testid={`deployment-effort-default-${displayAlias}`}
					/>
				</FieldRow>
			</div>

			<div className="bg-background/70 flex flex-wrap items-center gap-1.5 rounded-md border px-2.5 py-2 text-xs">
				<span className="text-muted-foreground font-medium">无感虚拟调用入口 (Mode A + B)：</span>
				<Badge variant="secondary" className="font-mono text-[11px]">
					{displayAlias}
				</Badge>
				{EFFORT_SUFFIXES.map((sfx) => (
					<Badge key={sfx} variant="outline" className="font-mono text-[11px]">
						{displayAlias}
						{sfx}
					</Badge>
				))}
			</div>
		</div>
	);
}

function ExpandedConfigPanel({
	aliasName,
	config,
	onChange,
	providerName,
	disabled,
	isPattern,
}: {
	aliasName?: string;
	config: AliasConfig;
	onChange: (patch: Partial<AliasConfig>) => void;
	providerName: string;
	disabled?: boolean;
	isPattern?: boolean;
}) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-6 border-t p-4">
			<EffortFusionSection
				aliasName={aliasName}
				config={config}
				onChange={onChange}
				providerName={providerName}
				disabled={disabled}
			/>
			<div className="space-y-4">
				<FieldRow
					label={t("providers.deployments.canonicalModelName")}
					hint={isPattern ? t("providers.deployments.canonicalModelNamePatternHint") : t("providers.deployments.canonicalModelNameHint")}
				>
					<StringField
						value={config.model_name}
						onChange={(v) => onChange({ model_name: v })}
						placeholder={
							isPattern ? t("providers.deployments.canonicalPatternPlaceholder") : t("providers.deployments.canonicalPlaceholder")
						}
						disabled={disabled}
					/>
				</FieldRow>
				<FieldRow label={t("providers.deployments.modelFamily")} hint={t("providers.deployments.modelFamilyHint")}>
					<Select
						value={config.model_family ?? "__none__"}
						onValueChange={(v) => onChange({ model_family: v === "__none__" ? undefined : (v as ModelFamily) })}
						disabled={disabled}
					>
						<SelectTrigger className="w-full">
							<SelectValue placeholder={t("providers.deployments.selectModelFamily")} />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="__none__">{t("virtualKeys.none")}</SelectItem>
							{ModelFamilyValues.map((f) => (
								<SelectItem key={f} value={f}>
									{f}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</FieldRow>
				<FieldRow label={t("modelCatalog.description")} hint={t("providers.deployments.descriptionHint")}>
					<Textarea
						value={config.description ?? ""}
						onChange={(e) => {
							const v = e.target.value;
							onChange({ description: v === "" ? undefined : v });
						}}
						placeholder={t("providers.deployments.descriptionPlaceholder")}
						rows={2}
						disabled={disabled}
					/>
				</FieldRow>
			</div>
			<ProviderSection providerName={providerName} config={config} onChange={onChange} disabled={disabled} />
		</div>
	);
}

export function DeploymentsTable({
	value,
	onChange,
	providerName,
	availableModels,
	onCleanSourceModels,
	disabled = false,
}: Props) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const normalized = useMemo(() => normalize(value), [value]);
	const rows: Row[] = useMemo(() => Object.entries(normalized).map(([name, config]) => ({ name, config })), [normalized]);

	const { data: catalogModelsData } = useGetModelsQuery(
		{
			provider: providerName || undefined,
			limit: 500,
			unfiltered: true,
		},
		{ skip: !providerName },
	);

	const candidateModelIds = useMemo(() => {
		const merged: string[] = [];
		if (catalogModelsData?.models) {
			for (const m of catalogModelsData.models) {
				if (m.name) merged.push(m.name);
			}
		}
		if (availableModels) {
			for (const m of availableModels) {
				if (m && m !== "*") merged.push(m);
			}
		}
		return merged;
	}, [catalogModelsData?.models, availableModels]);

	const detectedFamilies = useMemo(
		() => detectSmartEffortFamilies(candidateModelIds, providerName),
		[candidateModelIds, providerName],
	);

	// Stable per-row id keyed by current deployment name. Survives rename so
	// expanded/pendingNames state stays attached to the same row, and gives
	// React a stable list key instead of array index.
	const rowIdsRef = useRef<Map<string, string>>(new Map());
	const ensureRowId = (name: string): string => {
		const existing = rowIdsRef.current.get(name);
		if (existing) return existing;
		const id =
			typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
				? crypto.randomUUID()
				: `row-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
		rowIdsRef.current.set(name, id);
		return id;
	};
	const rowsWithIds = useMemo(() => rows.map((r) => ({ ...r, rowId: ensureRowId(r.name) })), [rows]);

	const [expanded, setExpanded] = useState<Set<string>>(new Set());
	const [draftExpanded, setDraftExpanded] = useState(false);
	const [draftRow, setDraftRow] = useState<Row>({ name: "", config: { model_id: "" } });
	// Per-row pending rename state, keyed by stable rowId. Keeps the input
	// controllable while a typed name collides with another committed row or is
	// empty — without this, we'd either snap the input back (jarring) or emit a
	// duplicate and silently drop a row.
	const [pendingNames, setPendingNames] = useState<Record<string, string>>({});

	const emit = (nextRows: Row[]) => {
		const out: Record<string, AliasConfig> = {};
		for (const r of nextRows) {
			if (!r.name.trim()) continue;
			out[r.name] = r.config;
		}
		onChange(out);
	};

	const updateRowByName = (oldName: string, patch: Partial<Row>) => {
		const next = rows.map((r) => (r.name === oldName ? { name: patch.name ?? r.name, config: patch.config ?? r.config } : r));
		emit(next);
	};

	const patchConfig = (name: string, patch: Partial<AliasConfig>) => {
		const current = rows.find((r) => r.name === name);
		if (!current) return;
		updateRowByName(name, { config: { ...current.config, ...patch } });
	};

	const removeRow = (rowId: string, name: string) => {
		emit(rows.filter((r) => r.name !== name));
		rowIdsRef.current.delete(name);
		setExpanded((prev) => {
			if (!prev.has(rowId)) return prev;
			const next = new Set(prev);
			next.delete(rowId);
			return next;
		});
		setPendingNames((prev) => {
			if (!(rowId in prev)) return prev;
			const { [rowId]: _drop, ...rest } = prev;
			return rest;
		});
	};

	const renameRow = (rowId: string, oldName: string, newName: string) => {
		const trimmed = newName.trim();
		const normalizedName = trimmed.toLowerCase();
		// Backend alias validation is case-insensitive and rejects leading/trailing
		// whitespace, so collision detection mirrors that to avoid UI-passes /
		// server-rejects splits.
		const collides = normalizedName !== "" && rows.some((r) => r.name !== oldName && r.name.trim().toLowerCase() === normalizedName);
		if (collides || trimmed === "") {
			setPendingNames((p) => ({ ...p, [rowId]: newName }));
			return;
		}
		setPendingNames((p) => {
			if (!(rowId in p)) return p;
			const { [rowId]: _drop, ...rest } = p;
			return rest;
		});
		// Transfer the stable rowId from old name → new name so per-row UI state
		// (expanded, pendingNames) survives the rename.
		rowIdsRef.current.delete(oldName);
		rowIdsRef.current.set(trimmed, rowId);
		const next = rows.map((r) => (r.name === oldName ? { name: trimmed, config: r.config } : r));
		emit(next);
	};

	const patchDraftConfig = (patch: Partial<AliasConfig>) => {
		setDraftRow((r) => {
			const next = { ...r, config: { ...r.config, ...patch } };
			if (next.name.trim() && (next.config.model_id.trim() || hasEffortModelsConfigured(next.config.effort_models))) {
				setTimeout(() => commitDraftIfReady(next), 0);
			}
			return next;
		});
	};

	const applyEffortPreset = () => {
		const sourceSet = new Set<string>();
		const baseIds: string[] = [];
		for (const fam of detectedFamilies) {
			baseIds.push(fam.baseId);
			for (const src of fam.sourceIds) {
				if (src.toLowerCase() !== fam.baseId.toLowerCase()) {
					sourceSet.add(src);
				}
			}
		}

		// Filter out any un-fused raw tier rows if the user had added them into deployments
		// (or outdated antigravity gemini-2.5-pro preset)
		const pLower = providerName.toLowerCase().trim();
		const nextRows = rows.filter((r) => {
			const nameLower = r.name.trim().toLowerCase();
			if (pLower === "antigravity" && nameLower === "gemini-2.5-pro") {
				return false;
			}
			if (
				!hasEffortModelsConfigured(r.config.effort_models) &&
				Array.from(sourceSet).some((s) => s.toLowerCase() === nameLower)
			) {
				return false;
			}
			return true;
		});

		const nextExpanded = new Set(expanded);
		for (const fam of detectedFamilies) {
			const presetCfg: AliasConfig = {
				model_id: fam.modelId,
				effort_models: fam.effortModels,
			};
			const existingIdx = nextRows.findIndex((r) => r.name.trim().toLowerCase() === fam.baseId.toLowerCase());
			if (existingIdx >= 0) {
				const existing = nextRows[existingIdx]!;
				nextRows[existingIdx] = {
					...existing,
					config: {
						...existing.config,
						model_id: fam.modelId,
						effort_models: fam.effortModels,
					},
				};
			} else {
				nextRows.push({ name: fam.baseId, config: presetCfg });
			}
		}
		emit(nextRows);
		setExpanded(nextExpanded);
		if (onCleanSourceModels && sourceSet.size > 0) {
			onCleanSourceModels([...sourceSet], baseIds);
		}
	};

	// Commit the in-progress draft row into the committed list. Called from
	// blur / Enter / model-selection. No-op when either field is missing — the
	// inline hint below the draft row warns the user before submit that a
	// partial entry will be dropped.
	const commitDraftIfReady = (override?: Row) => {
		const candidate = override ?? draftRow;
		const name = candidate.name.trim();
		const modelId = candidate.config.model_id.trim();
		const hasEffort = hasEffortModelsConfigured(candidate.config.effort_models);
		if (!name || (!modelId && !hasEffort)) return;
		const exists = Object.keys(normalized).some((k) => k.trim().toLowerCase() === name.toLowerCase());
		if (exists) return;
		const resolvedModelId =
			modelId ||
			candidate.config.effort_models?.default ||
			candidate.config.effort_models?.high ||
			candidate.config.effort_models?.medium ||
			candidate.config.effort_models?.low ||
			"";
		emit([...rows, { name, config: { ...candidate.config, model_id: resolvedModelId } }]);
		setDraftRow({ name: "", config: { model_id: "" } });
		setDraftExpanded(false);
	};

	const draftPatternError = draftRow.config.model_id.trim()
		? validatePatternAlias(draftRow.name.trim(), draftRow.config.model_id.trim(), draftRow.config.model_name)
		: null;

	const toggleExpanded = (rowId: string) => {
		setExpanded((prev) => {
			const next = new Set(prev);
			if (next.has(rowId)) next.delete(rowId);
			else next.add(rowId);
			return next;
		});
	};

	return (
		<div className="space-y-2">
			<div className="bg-muted/30 flex flex-col gap-2 rounded-md border px-3 py-2.5 text-xs">
				<div className="flex flex-wrap items-center justify-between gap-2">
					<div className="flex flex-wrap items-center gap-1.5">
						<Badge variant="outline" className="gap-1 text-[10px]">
							<Sparkles className="h-3 w-3" />
							智能 Effort 融合已开启 (Mode A + B)
						</Badge>
						<span className="text-muted-foreground">
							支持零配置自动识别渠道 <code>-low/-medium/-high/-thinking</code> 模型系列，以及直接附加{" "}
							<code>-minimal/-low/-medium/-high/-xhigh/-max</code> 虚拟后缀无感调用。
						</span>
					</div>
					{detectedFamilies.length > 0 && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="h-7 gap-1.5 text-xs"
							disabled={disabled}
							onClick={applyEffortPreset}
							data-testid="deployment-apply-effort-preset"
						>
							<Wand2 className="h-3.5 w-3.5" />
							智能识别并融合 ({detectedFamilies.length} 组)
						</Button>
					)}
				</div>
				{detectedFamilies.length > 0 && (
					<div className="flex flex-wrap items-center gap-1.5 pt-0.5">
						<span className="text-muted-foreground text-[11px]">从当前渠道模型列表智能识别到的融合系列：</span>
						{detectedFamilies.map((fam) => {
							const isApplied = rows.some(
								(r) =>
									r.name.trim().toLowerCase() === fam.baseId.toLowerCase() &&
									hasEffortModelsConfigured(r.config.effort_models),
							);
							return (
								<Badge
									key={fam.baseId}
									variant={isApplied ? "secondary" : "outline"}
									className="font-mono text-[10px]"
									title={`Low: ${fam.effortModels.low} | Medium: ${fam.effortModels.medium} | High: ${fam.effortModels.high}`}
								>
									{fam.baseId}
									{isApplied ? " ✓" : ""}
								</Badge>
							);
						})}
					</div>
				)}
			</div>
			<div className="overflow-hidden rounded-md border">
				<div className="bg-muted/50 text-foreground grid h-10 grid-cols-[28px_minmax(0,1fr)_minmax(0,1fr)_28px] items-center gap-2 border-b px-4 text-sm font-medium">
					<div />
					<div>{t("providers.deployments.deploymentName")}</div>
					<div>{t("providers.deployments.modelId")}</div>
					<span className="sr-only">{tc("actions")}</span>
				</div>
				<div className="divide-y">
					{rowsWithIds.map((row) => {
						const isOpen = expanded.has(row.rowId);
						const pending = pendingNames[row.rowId];
						const isPattern = isRegexEntry(row.name);
						const patternError = pending === undefined ? validatePatternAlias(row.name, row.config.model_id, row.config.model_name) : null;
						const rowHasEffort = hasEffortModelsConfigured(row.config.effort_models);
						return (
							<Collapsible key={row.rowId} open={isOpen} onOpenChange={() => toggleExpanded(row.rowId)}>
								<div className={cn(isOpen && "bg-muted/20")}>
									<div className="grid grid-cols-[28px_minmax(0,1fr)_minmax(0,1fr)_28px] items-center gap-2 px-2 py-1.5">
										<CollapsibleTrigger asChild>
											<Button
												variant="ghost"
												size="icon"
												className="h-7 w-7"
												disabled={disabled}
												data-testid={`deployment-expand-${row.name}`}
											>
												{isOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
											</Button>
										</CollapsibleTrigger>
										<div className="space-y-1">
											<div className="flex items-center gap-1.5">
												<div className="relative flex-1">
													<Input
														value={pending ?? row.name}
														onChange={(e) => renameRow(row.rowId, row.name, e.target.value)}
														placeholder={t("providers.deployments.requestModelName")}
														disabled={disabled}
														className={cn(isPattern && "pr-11")}
														data-testid={`deployment-name-${row.name}`}
													/>
													{isPattern && <PatternMark testId={`deployment-pattern-${row.name}`} />}
												</div>
												{rowHasEffort && (
													<Badge variant="secondary" className="shrink-0 gap-1 text-[10px]" title="已启用 Mode A 档位聚合与 Mode B 虚拟后缀路由">
														<Layers className="h-2.5 w-2.5" />
														Effort 融合
													</Badge>
												)}
											</div>
											{pending !== undefined && (
												<p className="text-destructive text-xs">
													{pending.trim() === "" ? t("providers.deployments.nameEmpty") : t("providers.deployments.nameExists")}
												</p>
											)}
											{patternError && (
												<p className="text-destructive text-xs" data-testid={`deployment-pattern-error-${row.name}`}>
													{patternError}
												</p>
											)}
										</div>
										<ModelSelector
											provider={providerName}
											value={row.config.model_id}
											onChange={(v) => patchConfig(row.name, { model_id: v })}
											placeholder={rowHasEffort ? "由 Effort 档位映射接管 (可选默认模型)" : t("providers.deployments.deploymentProfileId")}
											disabled={disabled}
											unfiltered
											allowCustomModel
											data-testid={`deployment-model-${row.name}`}
										/>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											className="text-muted-foreground hover:text-destructive h-7 w-7"
											onClick={() => removeRow(row.rowId, row.name)}
											disabled={disabled}
											data-testid={`deployment-delete-${row.name}`}
										>
											<Trash className="h-4 w-4" />
										</Button>
									</div>
									<CollapsibleContent>
										<ExpandedConfigPanel
											aliasName={row.name}
											config={row.config}
											onChange={(patch) => patchConfig(row.name, patch)}
											providerName={providerName}
											disabled={disabled}
											isPattern={isPattern}
										/>
									</CollapsibleContent>
								</div>
							</Collapsible>
						);
					})}
					<Collapsible open={draftExpanded} onOpenChange={setDraftExpanded}>
						<div className={cn(draftExpanded && "bg-muted/20")}>
							<div className="grid grid-cols-[28px_minmax(0,1fr)_minmax(0,1fr)_28px] items-center gap-2 px-2 py-1.5">
								<CollapsibleTrigger asChild>
									<Button variant="ghost" size="icon" className="h-7 w-7" disabled={disabled} data-testid="draft-deployment-expand">
										{draftExpanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
									</Button>
								</CollapsibleTrigger>
								<div className="relative">
									<Input
										value={draftRow.name}
										onChange={(e) => setDraftRow((r) => ({ ...r, name: e.target.value }))}
										onBlur={() => commitDraftIfReady()}
										onKeyDown={(e) => {
											if (e.key === "Enter") {
												e.preventDefault();
												commitDraftIfReady();
											}
										}}
										placeholder={t("providers.deployments.requestModelName")}
										disabled={disabled}
										className={cn(isRegexEntry(draftRow.name.trim()) && "pr-11")}
										data-testid="draft-deployment-name"
									/>
									{isRegexEntry(draftRow.name.trim()) && <PatternMark testId="draft-deployment-pattern" />}
								</div>
								<ModelSelector
									provider={providerName}
									value={draftRow.config.model_id}
									onChange={(modelId) => {
										const nextDraft = { ...draftRow, config: { ...draftRow.config, model_id: modelId } };
										setDraftRow(nextDraft);
										commitDraftIfReady(nextDraft);
									}}
									placeholder={t("providers.deployments.deploymentProfileId")}
									disabled={disabled}
									unfiltered
									allowCustomModel
									data-testid="draft-deployment-model"
								/>
								<div />
							</div>
							{draftPatternError && (
								<p className="text-destructive px-4 pb-2 text-xs" data-testid="draft-deployment-pattern-error">
									{draftPatternError}
								</p>
							)}
							{(draftRow.name.trim() !== "" || draftRow.config.model_id.trim() !== "") &&
								!(draftRow.name.trim() && (draftRow.config.model_id.trim() || hasEffortModelsConfigured(draftRow.config.effort_models))) && (
									<p className="text-muted-foreground px-4 pb-2 text-xs">{t("providers.deployments.draftIncomplete")}</p>
								)}
							<CollapsibleContent>
								<ExpandedConfigPanel
									aliasName={draftRow.name}
									config={draftRow.config}
									onChange={patchDraftConfig}
									providerName={providerName}
									disabled={disabled}
									isPattern={isRegexEntry(draftRow.name.trim())}
								/>
							</CollapsibleContent>
						</div>
					</Collapsible>
				</div>
				<p className="text-muted-foreground border-t px-4 py-2 text-xs">{t("providers.deployments.patternHint")}</p>
			</div>
		</div>
	);
}