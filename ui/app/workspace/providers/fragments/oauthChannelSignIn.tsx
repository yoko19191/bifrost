import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
	getErrorMessage,
	useCompleteOAuthChannelSignInMutation,
	useGetOAuthChannelQuotaMutation,
	useImportOAuthChannelCredentialMutation,
	useStartOAuthChannelSignInMutation,
} from "@/lib/store";
import type { OAuthCredentialResult, OAuthQuotaObservation, OAuthStartResponse } from "@/lib/types/config";
import { isRedacted } from "@/lib/utils/validation";
import {
	Activity,
	Check,
	CheckCircle2,
	Copy,
	ExternalLink,
	FileJson,
	KeyRound,
	Loader2,
	RefreshCw,
	Sparkles,
	Upload,
	UserCheck,
} from "lucide-react";
import { ChangeEvent, useEffect, useMemo, useRef, useState } from "react";

export const OAUTH_CHANNEL_PROVIDERS = ["antigravity", "codex", "claude", "grok"] as const;
export type OAuthChannelProvider = (typeof OAUTH_CHANNEL_PROVIDERS)[number];

export function isOAuthChannelProvider(provider: string | undefined): provider is OAuthChannelProvider {
	if (!provider) return false;
	return OAUTH_CHANNEL_PROVIDERS.includes(provider.toLowerCase().trim() as OAuthChannelProvider);
}

interface ParsedCredentialMeta {
	type?: string;
	email?: string;
	account_id?: string;
	project_id?: string;
	plan_type?: string;
	expired?: string;
}

function parseCredentialPreview(raw: string | undefined): ParsedCredentialMeta | null {
	if (!raw || !raw.trim() || isRedacted(raw)) return null;
	try {
		const obj = JSON.parse(raw);
		if (!obj || typeof obj !== "object") return null;
		return {
			type: typeof obj.type === "string" ? obj.type : undefined,
			email: typeof obj.email === "string" ? obj.email : undefined,
			account_id: typeof obj.account_id === "string" ? obj.account_id : undefined,
			project_id: typeof obj.project_id === "string" ? obj.project_id : undefined,
			plan_type: typeof obj.plan_type === "string" ? obj.plan_type : undefined,
			expired: typeof obj.expired === "string" ? obj.expired : undefined,
		};
	} catch {
		return null;
	}
}

const PROVIDER_DISPLAY_META: Record<
	OAuthChannelProvider,
	{
		title: string;
		subtitle: string;
		callbackPlaceholder: string;
		callbackHelp: string;
	}
> = {
	antigravity: {
		title: "Antigravity (Google Cloud Code OAuth)",
		subtitle: "Sign in with Google or import an Antigravity OAuth credential JSON to proxy Claude, Gemini, and GPT models.",
		callbackPlaceholder: "http://localhost:51121/oauth-callback?code=4/0A...&state=...",
		callbackHelp:
			"After approving access in your browser, copy the redirected http://localhost:51121/oauth-callback?... URL from your browser address bar and paste it here.",
	},
	codex: {
		title: "OpenAI Codex Subscription (OAuth)",
		subtitle: "Sign in with your ChatGPT Plus / Pro / Team account or import a Codex OAuth credential JSON.",
		callbackPlaceholder: "http://localhost:1455/auth/callback?code=...&state=...",
		callbackHelp:
			"After signing in with OpenAI in your browser, copy the redirected http://localhost:1455/auth/callback?... URL from the address bar and paste it here.",
	},
	claude: {
		title: "Claude Subscription (OAuth)",
		subtitle: "Sign in with your Claude Pro / Max / Team account or import a Claude OAuth credential JSON.",
		callbackPlaceholder: "http://localhost:54545/callback?code=...&state=... or code#state",
		callbackHelp:
			"After authorizing on claude.ai, paste either the redirected callback URL or the code#state value shown on the page.",
	},
	grok: {
		title: "xAI Grok Subscription (Device OAuth)",
		subtitle: "Authorize via xAI Device Code flow or import a Grok OAuth credential JSON.",
		callbackPlaceholder: "",
		callbackHelp: "",
	},
};

interface Props {
	provider: string;
	credentialValue?: string;
	keyId?: string;
	keyName?: string;
	onCredentialResolved: (result: OAuthCredentialResult) => void;
	onApplyModels?: (models: string[]) => void;
}

export function OAuthChannelSignIn({
	provider,
	credentialValue,
	keyId,
	keyName,
	onCredentialResolved,
	onApplyModels,
}: Props) {
	const normalizedProvider = (provider.toLowerCase().trim() as OAuthChannelProvider) || "antigravity";
	const meta = PROVIDER_DISPLAY_META[normalizedProvider] ?? PROVIDER_DISPLAY_META.antigravity;

	const [activeTab, setActiveTab] = useState<"oauth" | "import">("oauth");
	const [startFlow, { isLoading: isStarting }] = useStartOAuthChannelSignInMutation();
	const [completeFlow, { isLoading: isCompleting }] = useCompleteOAuthChannelSignInMutation();
	const [importCred, { isLoading: isImporting }] = useImportOAuthChannelCredentialMutation();
	const [fetchQuota, { isLoading: isFetchingQuota }] = useGetOAuthChannelQuotaMutation();

	const [flowSession, setFlowSession] = useState<OAuthStartResponse | null>(null);
	const [callbackInput, setCallbackInput] = useState("");
	const [importText, setImportText] = useState("");
	const [lastResult, setLastResult] = useState<OAuthCredentialResult | null>(null);
	const [quota, setQuota] = useState<OAuthQuotaObservation | null>(null);
	const [error, setError] = useState<string | null>(null);

	const { copy, copied } = useCopyToClipboard();
	const fileInputRef = useRef<HTMLInputElement | null>(null);
	const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const generationRef = useRef(0);

	const stopPolling = () => {
		generationRef.current++;
		if (pollTimerRef.current) {
			clearTimeout(pollTimerRef.current);
			pollTimerRef.current = null;
		}
	};

	useEffect(() => stopPolling, []);

	const parsedPreview = useMemo(() => parseCredentialPreview(credentialValue), [credentialValue]);

	const effectiveIdentity = useMemo(() => {
		return {
			email: quota?.email || lastResult?.email || parsedPreview?.email,
			account_id: quota?.account_id || lastResult?.account_id || parsedPreview?.account_id,
			project_id: quota?.project_id || lastResult?.project_id || parsedPreview?.project_id,
			plan_type: quota?.plan_type || lastResult?.plan_type || parsedPreview?.plan_type,
			expire: lastResult?.expire || parsedPreview?.expired,
		};
	}, [quota, lastResult, parsedPreview]);

	const hasStoredOrInlineCredential =
		Boolean(credentialValue && credentialValue.trim() !== "") || Boolean(keyId) || Boolean(keyName);

	const pollDeviceFlow = (session: OAuthStartResponse, intervalSec: number) => {
		if (!session.device_state) return;
		const gen = generationRef.current;
		pollTimerRef.current = setTimeout(async () => {
			try {
				const res = await completeFlow({
					provider: normalizedProvider,
					device_state: session.device_state,
				}).unwrap();
				if (gen !== generationRef.current) return;
				if (res.status === "pending" || res.status === "slow_down") {
					pollDeviceFlow(session, res.interval || intervalSec);
					return;
				}
				setFlowSession(null);
				if (res.status === "authorized" && res.credential_json) {
					setLastResult(res);
					onCredentialResolved(res);
				} else {
					setError(`Device authorization ended with status: ${res.status}`);
				}
			} catch (err) {
				if (gen !== generationRef.current) return;
				setFlowSession(null);
				setError(getErrorMessage(err));
			}
		}, Math.max(intervalSec, 2) * 1000);
	};

	const pollBrowserFlow = (session: OAuthStartResponse, intervalSec: number) => {
		if (!session.state) return;
		const gen = generationRef.current;
		pollTimerRef.current = setTimeout(async () => {
			try {
				const res = await completeFlow({
					provider: normalizedProvider,
					expected_state: session.state,
					verifier: session.verifier,
				}).unwrap();
				if (gen !== generationRef.current) return;
				if (res.callback_url) {
					setCallbackInput(res.callback_url);
				}
				if (res.status === "pending") {
					pollBrowserFlow(session, res.interval || intervalSec);
					return;
				}
				if (res.status === "callback_captured") {
					if (res.error) {
						setError(`回调 URL 已自动填入，但后台自动换取 Token 失败 (${res.error})，请检查网络/代理或直接点击 Complete 重试。`);
					}
					return;
				}
				if (res.status === "authorized" && res.credential_json) {
					setFlowSession(null);
					setLastResult(res);
					onCredentialResolved(res);
				}
			} catch (err) {
				if (gen !== generationRef.current) return;
				setError(getErrorMessage(err));
			}
		}, Math.max(intervalSec, 2) * 1000);
	};

	const handleStartOAuth = async () => {
		stopPolling();
		setError(null);
		const gen = generationRef.current;
		try {
			const session = await startFlow({ provider: normalizedProvider }).unwrap();
			if (gen !== generationRef.current) return;
			setFlowSession(session);
			setCallbackInput("");
			if (session.flow_type === "browser" && session.auth_url) {
				window.open(session.auth_url, "_blank", "noopener,noreferrer");
				pollBrowserFlow(session, 2);
			} else if (session.flow_type === "device" && session.device_state) {
				pollDeviceFlow(session, session.device_state.interval_seconds || 5);
			}
		} catch (err) {
			if (gen !== generationRef.current) return;
			setError(getErrorMessage(err));
		}
	};

	const handleCompleteBrowserOAuth = async () => {
		if (!flowSession || !callbackInput.trim()) return;
		setError(null);
		try {
			const res = await completeFlow({
				provider: normalizedProvider,
				callback_url: callbackInput.trim(),
				expected_state: flowSession.state,
				verifier: flowSession.verifier,
			}).unwrap();
			if (res.status === "authorized" && res.credential_json) {
				setLastResult(res);
				setFlowSession(null);
				setCallbackInput("");
				onCredentialResolved(res);
			} else {
				setError(`Authorization returned status: ${res.status}`);
			}
		} catch (err) {
			setError(getErrorMessage(err));
		}
	};

	const handleImportJSON = async (rawText?: string) => {
		const payload = (rawText ?? importText).trim();
		if (!payload) return;
		setError(null);
		try {
			const res = await importCred({
				provider: normalizedProvider,
				raw_json: payload,
			}).unwrap();
			if (res.credential_json) {
				setLastResult(res);
				setImportText("");
				onCredentialResolved(res);
			}
		} catch (err) {
			setError(getErrorMessage(err));
		}
	};

	const handleFileUpload = async (e: ChangeEvent<HTMLInputElement>) => {
		const file = e.target.files?.[0];
		if (!file) return;
		try {
			const text = await file.text();
			setImportText(text);
			await handleImportJSON(text);
		} catch (err) {
			setError(getErrorMessage(err));
		} finally {
			if (fileInputRef.current) {
				fileInputRef.current.value = "";
			}
		}
	};

	const handleCheckQuota = async () => {
		setError(null);
		try {
			const inlineCred =
				credentialValue && !isRedacted(credentialValue) && credentialValue.trim() !== ""
					? credentialValue.trim()
					: lastResult?.credential_json;
			const obs = await fetchQuota({
				provider: normalizedProvider,
				credential: inlineCred,
				key_id: keyId,
				key_name: keyName,
			}).unwrap();
			setQuota(obs);
		} catch (err) {
			setError(getErrorMessage(err));
		}
	};

	return (
		<div className="space-y-4 rounded-md border bg-muted/15 p-4" data-testid={`oauth-channel-panel-${normalizedProvider}`}>
			<div className="flex flex-wrap items-start justify-between gap-2">
				<div className="space-y-1">
					<div className="flex items-center gap-2">
						<KeyRound className="text-primary h-4 w-4" />
						<h4 className="text-sm font-semibold">{meta.title}</h4>
						<Badge variant="outline" className="text-xs uppercase">
							OAuth Channel
						</Badge>
					</div>
					<p className="text-muted-foreground text-xs">{meta.subtitle}</p>
				</div>
				{hasStoredOrInlineCredential && (
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={handleCheckQuota}
						disabled={isFetchingQuota}
						data-testid="oauth-channel-check-quota-btn"
					>
						{isFetchingQuota ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <Activity className="mr-1.5 h-3.5 w-3.5" />}
						Check Live Quota & Models
					</Button>
				)}
			</div>

			{/* Account Identity Card when credential metadata is available */}
			{(effectiveIdentity.email ||
				effectiveIdentity.account_id ||
				effectiveIdentity.project_id ||
				effectiveIdentity.plan_type ||
				effectiveIdentity.expire) && (
				<div
					className="bg-background flex flex-wrap items-center justify-between gap-3 rounded-md border p-3 text-xs"
					data-testid="oauth-channel-identity-card"
				>
					<div className="flex flex-wrap items-center gap-3">
						<span className="inline-flex items-center gap-1.5 font-medium text-emerald-600 dark:text-emerald-400">
							<UserCheck className="h-4 w-4" />
							{effectiveIdentity.email || effectiveIdentity.account_id || "Authorized Account"}
						</span>
						{effectiveIdentity.plan_type && (
							<Badge variant="secondary" className="uppercase">
								Plan: {effectiveIdentity.plan_type}
							</Badge>
						)}
						{effectiveIdentity.project_id && (
							<span className="text-muted-foreground font-mono">Project: {effectiveIdentity.project_id}</span>
						)}
						{!effectiveIdentity.project_id && effectiveIdentity.account_id && (
							<span className="text-muted-foreground font-mono">Account: {effectiveIdentity.account_id}</span>
						)}
					</div>
					{effectiveIdentity.expire && (
						<span className="text-muted-foreground">
							Token Expires: <span className="font-mono">{new Date(effectiveIdentity.expire).toLocaleString()}</span> (Auto-Refreshes)
						</span>
					)}
				</div>
			)}

			<Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as "oauth" | "import")} className="w-full">
				<TabsList className="grid w-full grid-cols-2">
					<TabsTrigger value="oauth" data-testid="oauth-channel-tab-signin">
						<ExternalLink className="mr-1.5 h-3.5 w-3.5" />
						{normalizedProvider === "grok" ? "Device Code Sign-In" : "Browser OAuth Sign-In"}
					</TabsTrigger>
					<TabsTrigger value="import" data-testid="oauth-channel-tab-import">
						<FileJson className="mr-1.5 h-3.5 w-3.5" />
						Import Credential JSON
					</TabsTrigger>
				</TabsList>

				<TabsContent value="oauth" className="space-y-3 pt-3">
					{!flowSession ? (
						<div className="flex flex-wrap items-center gap-3">
							<Button
								type="button"
								variant="default"
								onClick={handleStartOAuth}
								disabled={isStarting}
								data-testid="oauth-channel-start-btn"
							>
								{isStarting ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <ExternalLink className="mr-2 h-4 w-4" />}
								{normalizedProvider === "grok" ? "Start xAI Device Sign-In" : `Sign in with ${meta.title.split(" ")[0]}`}
							</Button>
							<span className="text-muted-foreground text-xs">
								Tokens are stored in this key and automatically refreshed before expiration.
							</span>
						</div>
					) : flowSession.flow_type === "browser" ? (
						<div className="bg-background space-y-3 rounded-md border p-3 text-sm" data-testid="oauth-channel-browser-steps">
							<div className="flex flex-wrap items-center justify-between gap-2">
								<span className="font-medium">Step 1: Authorize in Browser</span>
								<div className="flex items-center gap-2">
									<Button
										type="button"
										variant="outline"
										size="sm"
										onClick={() => window.open(flowSession.auth_url, "_blank", "noopener,noreferrer")}
									>
										<ExternalLink className="mr-1.5 h-3.5 w-3.5" />
										Open Authorization Page
									</Button>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										onClick={() => copy(flowSession.auth_url || "")}
										data-testid="oauth-channel-copy-auth-url"
									>
										{copied ? <Check className="mr-1 h-3.5 w-3.5" /> : <Copy className="mr-1 h-3.5 w-3.5" />}
										Copy Link
									</Button>
								</div>
							</div>
							<div className="space-y-1.5">
								<label className="text-xs font-medium">Step 2: Paste Redirect Callback URL or Code</label>
								<p className="text-muted-foreground text-xs">{meta.callbackHelp}</p>
								<div className="flex gap-2">
									<Input
										value={callbackInput}
										onChange={(e) => setCallbackInput(e.target.value)}
										placeholder={meta.callbackPlaceholder}
										className="font-mono text-xs"
										data-testid="oauth-channel-callback-input"
									/>
									<Button
										type="button"
										onClick={handleCompleteBrowserOAuth}
										disabled={isCompleting || !callbackInput.trim()}
										data-testid="oauth-channel-complete-btn"
									>
										{isCompleting ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : <CheckCircle2 className="mr-1.5 h-4 w-4" />}
										Complete
									</Button>
									<Button
										type="button"
										variant="ghost"
										onClick={() => {
											stopPolling();
											setFlowSession(null);
										}}
									>
										Cancel
									</Button>
								</div>
							</div>
						</div>
					) : (
						<div className="bg-background space-y-3 rounded-md border p-3 text-sm" data-testid="oauth-channel-device-steps">
							<div className="flex flex-wrap items-center justify-between gap-2">
								<div className="space-y-1">
									<p className="font-medium">
										1. Open{" "}
										<a
											href={flowSession.device_state?.verification_uri_complete || flowSession.device_state?.verification_uri}
											target="_blank"
											rel="noopener noreferrer"
											className="text-primary inline-flex items-center gap-1 underline"
										>
											{flowSession.device_state?.verification_uri}
											<ExternalLink className="h-3 w-3" />
										</a>
									</p>
									<div className="flex items-center gap-2">
										<span>2. Enter device code:</span>
										<code className="bg-muted rounded px-2 py-0.5 font-mono text-base font-semibold">
											{flowSession.device_state?.user_code}
										</code>
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={() => copy(flowSession.device_state?.user_code || "")}
										>
											{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
										</Button>
									</div>
								</div>
								<div className="flex items-center gap-2">
									<span className="text-muted-foreground inline-flex items-center gap-1.5 text-xs">
										<Loader2 className="h-3.5 w-3.5 animate-spin" />
										Waiting for approval...
									</span>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										onClick={() => {
											stopPolling();
											setFlowSession(null);
										}}
									>
										Cancel
									</Button>
								</div>
							</div>
						</div>
					)}
				</TabsContent>

				<TabsContent value="import" className="space-y-3 pt-3">
					<div className="flex flex-wrap items-center justify-between gap-2">
						<span className="text-muted-foreground text-xs">
							Upload a <code>.json</code> credential file or paste its JSON contents below.
						</span>
						<div>
							<input
								ref={fileInputRef}
								type="file"
								accept=".json,application/json"
								className="hidden"
								onChange={handleFileUpload}
							/>
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => fileInputRef.current?.click()}
								disabled={isImporting}
								data-testid="oauth-channel-upload-json-btn"
							>
								<Upload className="mr-1.5 h-3.5 w-3.5" />
								Upload JSON File
							</Button>
						</div>
					</div>
					<Textarea
						value={importText}
						onChange={(e) => setImportText(e.target.value)}
						placeholder={`{"type": "${normalizedProvider}", "access_token": "...", "refresh_token": "...", ...}`}
						rows={4}
						className="font-mono text-xs"
						data-testid="oauth-channel-import-textarea"
					/>
					<div className="flex justify-end">
						<Button
							type="button"
							size="sm"
							onClick={() => handleImportJSON()}
							disabled={isImporting || !importText.trim()}
							data-testid="oauth-channel-import-submit-btn"
						>
							{isImporting ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="mr-1.5 h-3.5 w-3.5" />}
							Validate & Import Credential
						</Button>
					</div>
				</TabsContent>
			</Tabs>

			{/* Live Quota & Models Observation Card */}
			{quota && (
				<div className="bg-background space-y-3 rounded-md border p-3" data-testid="oauth-channel-quota-card">
					<div className="flex flex-wrap items-center justify-between gap-2 border-b pb-2">
						<div className="flex items-center gap-2">
							<span className="text-xs font-semibold">Live Subscription Quota</span>
							<Badge variant={quota.rate_limited ? "destructive" : "secondary"} className="text-xs">
								{quota.rate_limited ? "Rate Limited" : "Healthy"}
							</Badge>
							{quota.plan_type && (
								<Badge variant="outline" className="text-xs uppercase">
									{quota.plan_type}
								</Badge>
							)}
							{quota.credits && (
								<Badge variant="outline" className="text-xs">
									Credits: {quota.credits.unlimited ? "Unlimited" : quota.credits.balance || (quota.credits.has_credits ? "Available" : "0")}
								</Badge>
							)}
						</div>
						<Button type="button" variant="ghost" size="sm" onClick={handleCheckQuota} disabled={isFetchingQuota}>
							<RefreshCw className={`h-3.5 w-3.5 ${isFetchingQuota ? "animate-spin" : ""}`} />
						</Button>
					</div>

					{quota.windows && quota.windows.length > 0 ? (
						<div className="grid gap-2.5 sm:grid-cols-2">
							{quota.windows.map((win) => {
								const used = Math.min(100, Math.max(0, Math.round(win.used_percent ?? (100 - (win.remaining_percent ?? 100)))));
								const remaining = Math.min(100, Math.max(0, Math.round(win.remaining_percent ?? 100 - used)));
								const barColor =
									remaining <= 10 ? "bg-destructive" : remaining <= 30 ? "bg-amber-500" : "bg-emerald-500";
								return (
									<div key={win.id} className="space-y-1 rounded border p-2 text-xs">
										<div className="flex items-center justify-between font-medium">
											<span>{win.label}</span>
											<span className="font-mono">{remaining}% remaining</span>
										</div>
										<div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
											<div className={`h-full transition-all ${barColor}`} style={{ width: `${remaining}%` }} />
										</div>
										{win.reset_at && (
											<div className="text-muted-foreground text-[11px]">
												Resets: {new Date(win.reset_at).toLocaleString()}
											</div>
										)}
									</div>
								);
							})}
						</div>
					) : (
						<p className="text-muted-foreground text-xs">No active rate-limit windows reported by upstream.</p>
					)}

					{quota.models && quota.models.length > 0 && (
						<div className="flex flex-wrap items-center justify-between gap-2 border-t pt-2 text-xs">
							<div className="text-muted-foreground">
								已从当前订阅动态发现 <span className="text-foreground font-semibold">{quota.models.length}</span> 个可用模型 (
								{quota.models.slice(0, 5).join(", ")}
								{quota.models.length > 5 ? `, +${quota.models.length - 5} more` : ""})
							</div>
							{onApplyModels && (
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => onApplyModels(quota.models || [])}
									data-testid="oauth-channel-sync-models-btn"
								>
									<Sparkles className="mr-1.5 h-3.5 w-3.5" />
									同步并智能融合模型
								</Button>
							)}
						</div>
					)}
				</div>
			)}

			{error && (
				<p className="text-destructive text-xs font-medium" data-testid="oauth-channel-error">
					{error}
				</p>
			)}
		</div>
	);
}
