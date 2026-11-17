package modelcatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

const (
	// DefaultBioToolsDir is the default host path to the bio-tools MCP suite.
	// Can be overridden via the BIFROST_BIO_TOOLS_DIR environment variable.
	DefaultBioToolsDir = "/Volumes/MacStore/project_tumbleweed-scientific/mcps/bio-tools"

	// ScientificDiscoveryCategory is the category used for built-in scientific MCP servers.
	ScientificDiscoveryCategory = "Scientific Discovery"
)

// ResolveBioToolsPaths returns the python interpreter and run_server.py script paths for bio-tools.
func ResolveBioToolsPaths() (pythonBin string, runnerScript string) {
	baseDir := strings.TrimSpace(os.Getenv("BIFROST_BIO_TOOLS_DIR"))
	if baseDir == "" {
		baseDir = DefaultBioToolsDir
	}
	venvPython := filepath.Join(baseDir, ".venv", "bin", "python")
	if _, err := os.Stat(venvPython); err == nil {
		pythonBin = venvPython
	} else {
		pythonBin = "python3"
	}
	runnerScript = filepath.Join(baseDir, "run_server.py")
	return pythonBin, runnerScript
}

type scientificMCPDef struct {
	Slug                string
	Name                string
	ClientName          string
	EndpointSlug        string
	PackageName         string
	Description         string
	Subcategory         string
	DocsURL             string
	Tags                []string
	OptionalEnvs        []string
	EnvHints            map[string]string
	Tools               []string
	RecommendedCodeMode bool
}

var builtinScientificMCPDefs = []scientificMCPDef{
	{
		Slug:                "bio-tools-all-in-one",
		Name:                "Bio-Tools: All-in-One (全量生命科学聚合网关)",
		ClientName:          "bio_all",
		EndpointSlug:        "bio",
		PackageName:         "mcp_bio",
		Description:         "单进程聚合全部 23 个生命科学领域的 232 个科研工具（涵盖 PubMed、ChEMBL、gnomAD、ClinVar、PDB、AlphaFold、GTEx、OpenTargets、ClinicalTrials 等）。默认推荐开启 Starlark Code Mode 以节省 95% 上下文 Token。HTTP 路径：/mcp/bio",
		Subcategory:         "全量聚合套件",
		DocsURL:             "https://modelcontextprotocol.io",
		Tags:                []string{"scientific", "bioinformatics", "all-in-one", "genomics", "chemistry", "clinical"},
		OptionalEnvs:        []string{"NCBI_API_KEY", "NCBI_EMAIL", "OPENALEX_API_KEY", "OPERON_CONTACT_EMAIL"},
		EnvHints: map[string]string{
			"NCBI_API_KEY":         "可选：NCBI E-utilities API Key（填入后 PubMed/ClinVar/dbSNP 限速从 3 req/s 提升至 10 req/s，留空使用免费公共配额）",
			"NCBI_EMAIL":           "可选：NCBI 推荐携带的联系邮箱（防止高频检索被限流，留空使用默认配置）",
			"OPENALEX_API_KEY":     "可选：OpenAlex Polite Pool API Key（提升学术引文检索速率，留空使用免费公共配额）",
			"OPERON_CONTACT_EMAIL": "可选：学术 API 礼貌池联系邮箱（用于 Crossref/OpenAlex/Unpaywall 礼貌池标识）",
		},
		RecommendedCodeMode: true,
		Tools: []string{
			"search_articles", "get_article_metadata", "get_full_text_article", "compound_search", "target_search",
			"get_bioactivity", "get_variant", "clinvar_search", "open_targets_disease_targets", "alphafold_get_prediction",
			"pdb_search_structures", "gtex_median_expression", "search_trials", "gwas_associations_for_gene",
		},
	},
	{
		Slug:         "bio-tools-pubmed",
		Name:         "Bio-Tools: PubMed & PMC (生物医学文献检索)",
		ClientName:   "bio_pubmed",
		EndpointSlug: "pubmed",
		PackageName:  "mcp_pubmed",
		Description:  "对接 NCBI PubMed 与 PMC 开放获取全文库，提供医学文献检索、元数据获取、PMID/PMCID/DOI 互转、相似文献推荐与全文提取。独立 HTTP 路径：/mcp/pubmed",
		Subcategory:  "文献与预印本",
		DocsURL:      "https://www.ncbi.nlm.nih.gov/pmc/tools/developers/",
		Tags:         []string{"scientific", "literature", "pubmed", "pmc", "ncbi"},
		OptionalEnvs: []string{"NCBI_API_KEY", "NCBI_EMAIL"},
		EnvHints: map[string]string{
			"NCBI_API_KEY": "可选：NCBI API Key（填入后检索限速从 3 次/秒提升至 10 次/秒，留空可直接用免费配额）",
			"NCBI_EMAIL":   "可选：NCBI E-utilities 联系邮箱（留空不影响正常使用）",
		},
		Tools: []string{
			"search_articles", "get_article_metadata", "get_full_text_article",
			"find_related_articles", "lookup_article_by_citation", "convert_article_ids", "get_copyright_status",
		},
	},
	{
		Slug:         "bio-tools-biorxiv",
		Name:         "Bio-Tools: bioRxiv & medRxiv (生命科学预印本)",
		ClientName:   "bio_biorxiv",
		EndpointSlug: "biorxiv",
		PackageName:  "mcp_biorxiv",
		Description:  "检索 bioRxiv 与 medRxiv 最新生物学与医学预印本、经费资助来源反查、预印本转正发表追踪及学科分类统计。免凭证即用，独立 HTTP 路径：/mcp/biorxiv",
		Subcategory:  "文献与预印本",
		DocsURL:      "https://api.biorxiv.org/",
		Tags:         []string{"scientific", "literature", "biorxiv", "medrxiv", "preprints"},
		Tools: []string{
			"search_preprints", "get_preprint", "search_published_preprints",
			"search_by_funder", "get_categories", "get_content_statistics", "get_usage_statistics",
		},
	},
	{
		Slug:         "bio-tools-literature",
		Name:         "Bio-Tools: OpenAlex & arXiv (跨学科学术与引文网络)",
		ClientName:   "bio_literature",
		EndpointSlug: "literature",
		PackageName:  "mcp_literature",
		Description:  "对接 OpenAlex 全球学术图谱与 arXiv 预印本库，支持论文检索、正向引用（Citations）、参考文献（References）、学者画像与期刊统计。独立 HTTP 路径：/mcp/literature",
		Subcategory:  "文献与预印本",
		DocsURL:      "https://docs.openalex.org/",
		Tags:         []string{"scientific", "literature", "openalex", "arxiv", "citations"},
		OptionalEnvs: []string{"OPENALEX_API_KEY", "OPERON_CONTACT_EMAIL"},
		EnvHints: map[string]string{
			"OPENALEX_API_KEY":     "可选：OpenAlex API Key（用于高频引文网络抓取，留空使用每日 10 万次免费公共池）",
			"OPERON_CONTACT_EMAIL": "可选：OpenAlex Polite Pool 联系邮箱（填入后自动进入高速礼貌池）",
		},
		Tools: []string{
			"openalex_search_works", "openalex_get_work", "openalex_citations", "openalex_references",
			"openalex_search_authors", "openalex_get_author", "openalex_venue_info", "arxiv_search", "arxiv_get_papers",
		},
	},
	{
		Slug:         "bio-tools-chembl",
		Name:         "Bio-Tools: ChEMBL (药物靶点与生物活性数据库)",
		ClientName:   "bio_chembl",
		EndpointSlug: "chembl",
		PackageName:  "mcp_chembl",
		Description:  "对接 EMBL-EBI ChEMBL 数据库，查询小分子化合物、药物靶点、IC50/Ki/EC50 生物活性测定数据、批准药物作用机制（MoA）与 ADMET 药代性质。免凭证即用，独立 HTTP 路径：/mcp/chembl",
		Subcategory:  "药物化学与监管",
		DocsURL:      "https://www.ebi.ac.uk/chembl/",
		Tags:         []string{"scientific", "chemistry", "chembl", "drug-discovery", "bioactivity"},
		Tools: []string{
			"compound_search", "target_search", "drug_search", "get_bioactivity", "get_mechanism", "get_admet",
		},
	},
	{
		Slug:         "bio-tools-chemistry",
		Name:         "Bio-Tools: PubChem & BindingDB & Rhea (小分子与反应化学)",
		ClientName:   "bio_chemistry",
		EndpointSlug: "chemistry",
		PackageName:  "mcp_chemistry",
		Description:  "整合 PubChem（化合物检索/2D Tanimoto 相似性搜索/生物活性汇总/GHS 安全分类）、ChEBI 化学本体、Rhea 生化反应方程与 BindingDB 蛋白-配体实测亲和力。独立 HTTP 路径：/mcp/chemistry",
		Subcategory:  "药物化学与监管",
		DocsURL:      "https://pubchem.ncbi.nlm.nih.gov/",
		Tags:         []string{"scientific", "chemistry", "pubchem", "bindingdb", "chebi", "rhea"},
		OptionalEnvs: []string{"NCBI_EMAIL"},
		EnvHints: map[string]string{
			"NCBI_EMAIL": "可选：PubChem PUG REST 礼貌池联系邮箱（留空不影响使用）",
		},
		Tools: []string{
			"pubchem_search_compounds", "pubchem_get_compounds", "pubchem_similarity_search",
			"pubchem_get_bioassay_summary", "pubchem_get_safety", "chebi_search", "chebi_get_entity",
			"chebi_get_ontology", "rhea_search_reactions", "rhea_get_reaction",
			"bindingdb_ligands_by_target", "bindingdb_targets_by_compound",
		},
	},
	{
		Slug:         "bio-tools-zinc",
		Name:         "Bio-Tools: ZINC22 (可采购商业化合物与 3D 构象库)",
		ClientName:   "bio_zinc",
		EndpointSlug: "zinc",
		PackageName:  "mcp_zinc",
		Description:  "对接 ZINC22 (CartBlanche22) 商业化合物库，支持按 ZINC ID、SMILES 相似性、供应商检索可采购分子及获取分子对接所需的 3D 构象数据。免凭证即用，独立 HTTP 路径：/mcp/zinc",
		Subcategory:  "药物化学与监管",
		DocsURL:      "https://cartblanche22.docking.org/",
		Tags:         []string{"scientific", "chemistry", "zinc22", "docking", "virtual-screening"},
		Tools: []string{
			"zinc_search_by_id", "zinc_search_by_smiles", "zinc_search_by_supplier", "zinc_get_3d", "zinc_random_sample",
		},
	},
	{
		Slug:         "bio-tools-drug-regulatory",
		Name:         "Bio-Tools: openFDA (FDA 批准药品与处方说明书监管库)",
		ClientName:   "bio_drug_regulatory",
		EndpointSlug: "drug-regulatory",
		PackageName:  "mcp_drug_regulatory",
		Description:  "对接美国 FDA 官方 openFDA 接口（Drugs@FDA 与 Drug Labels），查询新药/仿制药审批记录（NDA/ANDA/BLA）、等效仿制药、药理分类及官方处方说明书（适应症、黑框警告、不良反应）。免凭证即用，独立 HTTP 路径：/mcp/drug-regulatory",
		Subcategory:  "药物化学与监管",
		DocsURL:      "https://open.fda.gov/",
		Tags:         []string{"scientific", "fda", "regulatory", "drugs", "pharmacology"},
		Tools: []string{
			"search_drug_applications", "get_drug_application", "count_drug_applications",
			"get_drug_statistics", "list_pharmacologic_classes", "get_generic_equivalents", "search_drug_labels",
		},
	},
	{
		Slug:         "bio-tools-variants",
		Name:         "Bio-Tools: gnomAD & ClinVar & dbSNP & CADD (人类遗传变异)",
		ClientName:   "bio_variants",
		EndpointSlug: "variants",
		PackageName:  "mcp_variants",
		Description:  "整合 gnomAD（人群等位基因频率、pLI/LOEUF 基因缺失不耐受约束、结构变异、线粒体变异）、ClinVar 临床致病性分类、dbSNP rsID 映射与 CADD 变异有害性评分。独立 HTTP 路径：/mcp/variants",
		Subcategory:  "基因组与临床变异",
		DocsURL:      "https://gnomad.broadinstitute.org/",
		Tags:         []string{"scientific", "genomics", "variants", "gnomad", "clinvar", "dbsnp", "cadd"},
		OptionalEnvs: []string{"NCBI_API_KEY"},
		EnvHints: map[string]string{
			"NCBI_API_KEY": "可选：NCBI API Key（提升 ClinVar 与 dbSNP 批量查询限速，留空使用公共免费配额）",
		},
		Tools: []string{
			"get_variant", "search_variants", "gene_variants", "gene_constraint", "region_variants",
			"liftover_variant", "clinvar_variants", "structural_variants", "get_structural_variant",
			"mitochondrial_variants", "cadd_variant_score", "cadd_position_scores", "cadd_range_scores",
			"clinvar_search", "clinvar_get_records", "clinvar_variant_by_rsid", "dbsnp_get_rsids", "dbsnp_search_by_region",
		},
	},
	{
		Slug:         "bio-tools-clinical-genomics",
		Name:         "Bio-Tools: ClinGen & CIViC & OpenTargets (临床基因组与靶点发现)",
		ClientName:   "bio_clinical_genomics",
		EndpointSlug: "clinical-genomics",
		PackageName:  "mcp_clinical_genomics",
		Description:  "整合 ClinGen（基因-疾病有效性、剂量敏感性、临床可干预性）、CIViC（肿瘤体细胞突变靶向药敏感/耐药临床证据）与 Open Targets（靶点-疾病关联评分与管线药物）。免凭证即用，独立 HTTP 路径：/mcp/clinical-genomics",
		Subcategory:  "基因组与临床变异",
		DocsURL:      "https://platform.opentargets.org/",
		Tags:         []string{"scientific", "clinical-genomics", "clingen", "civic", "opentargets", "oncology"},
		Tools: []string{
			"clingen_gene_validity", "clingen_dosage_sensitivity", "clingen_actionability",
			"clingen_variant_classifications", "civic_search_genes", "civic_gene_variants", "civic_get_variant",
			"civic_search_variants", "civic_get_evidence_item", "civic_search_evidence", "civic_get_assertion",
			"civic_search_assertions", "civic_get_molecular_profile", "civic_search_molecular_profiles",
			"civic_search_diseases", "civic_search_therapies", "open_targets_graphql",
			"open_targets_disease_drugs", "open_targets_disease_targets", "open_targets_drug",
		},
	},
	{
		Slug:         "bio-tools-human-genetics",
		Name:         "Bio-Tools: GWAS Catalog & FinnGen (人类遗传关联与 PheWAS)",
		ClientName:   "bio_human_genetics",
		EndpointSlug: "human-genetics",
		PackageName:  "mcp_human_genetics",
		Description:  "对接 NHGRI-EBI GWAS Catalog、eQTL Catalogue 与 PheWeb/FinnGen 生物银行，检索变异/基因/表型的全基因组显著关联（GWAS）与跨表型关联（PheWAS）。免凭证即用，独立 HTTP 路径：/mcp/human-genetics",
		Subcategory:  "基因组与临床变异",
		DocsURL:      "https://www.ebi.ac.uk/gwas/",
		Tags:         []string{"scientific", "genetics", "gwas", "phewas", "finngen", "eqtl"},
		Tools: []string{
			"gwas_associations_for_variant", "gwas_associations_for_gene", "gwas_associations_for_trait",
			"gwas_search_traits", "gwas_search_studies", "gwas_get_study", "gwas_get_variant",
			"eqtl_list_datasets", "eqtl_associations", "phewas_instances", "phewas_variant",
			"phewas_finngen_gene", "phewas_list_phenotypes", "phewas_search_phenotypes",
		},
	},
	{
		Slug:         "bio-tools-genomes",
		Name:         "Bio-Tools: Ensembl & UCSC (基因组序列、VEP 注释与进化保守性)",
		ClientName:   "bio_genomes",
		EndpointSlug: "genomes",
		PackageName:  "mcp_genomes",
		Description:  "对接 Ensembl REST API 与 UCSC Genome Browser，支持基因/转录本查询、外部 ID 映射、VEP 变异效应预测、跨物种同源基因、基因组序列获取及进化保守性轨道。免凭证即用，独立 HTTP 路径：/mcp/genomes",
		Subcategory:  "基因组与临床变异",
		DocsURL:      "https://rest.ensembl.org/",
		Tags:         []string{"scientific", "genomics", "ensembl", "ucsc", "vep", "sequence"},
		Tools: []string{
			"ensembl_lookup", "ensembl_xrefs", "ensembl_vep_variant", "ensembl_homology",
			"ensembl_sequence", "ensembl_overlap_region", "ucsc_list_tracks", "ucsc_track_data",
			"ucsc_conservation", "ucsc_tfbs_clusters", "ucsc_chrom_sizes",
		},
	},
	{
		Slug:         "bio-tools-structures-interactions",
		Name:         "Bio-Tools: PDB & AlphaFold & EMDB & IntAct (3D 结构生物学与互作)",
		ClientName:   "bio_structures_interactions",
		EndpointSlug: "structures-interactions",
		PackageName:  "mcp_structures_interactions",
		Description:  "整合 RCSB PDB 实验测定结构、AlphaFold DB 预测结构与 pLDDT 置信度、EMDB 冷冻电镜密度图、ComplexPortal 大分子复合物及 IntAct 分子互作网络。免凭证即用，独立 HTTP 路径：/mcp/structures-interactions",
		Subcategory:  "蛋白结构与通路",
		DocsURL:      "https://www.rcsb.org/",
		Tags:         []string{"scientific", "structural-biology", "pdb", "alphafold", "emdb", "intact"},
		Tools: []string{
			"pdb_search_structures", "pdb_get_structures", "pdb_get_entities", "pdb_get_ligands",
			"alphafold_get_prediction", "alphafold_check_coverage", "emdb_get_entries", "emdb_search_entries",
			"emdb_get_entry_section", "emdb_get_validation", "complexportal_get_complexes",
			"complexportal_search_by_participant", "intact_fetch_interactions", "intact_get_interactor",
			"intact_get_interaction_details", "intact_build_network",
		},
	},
	{
		Slug:         "bio-tools-protein-annotation",
		Name:         "Bio-Tools: InterPro & HPA & STRING (蛋白质结构域、定位与互作网络)",
		ClientName:   "bio_protein_annotation",
		EndpointSlug: "protein-annotation",
		PackageName:  "mcp_protein_annotation",
		Description:  "整合 InterPro/Pfam 蛋白质结构域与家族注释、Human Protein Atlas (HPA) 组织与亚细胞空间表达定位、以及 STRING 蛋白质-蛋白质互作（PPI）网络。免凭证即用，独立 HTTP 路径：/mcp/protein-annotation",
		Subcategory:  "蛋白结构与通路",
		DocsURL:      "https://www.ebi.ac.uk/interpro/",
		Tags:         []string{"scientific", "proteomics", "interpro", "pfam", "string-db", "protein-atlas"},
		Tools: []string{
			"get_domain_architecture", "search_interpro_entries", "get_interpro_entry",
			"search_pfam_clans", "get_pfam_clan", "get_pfam_family_proteins", "get_pfam_family_proteomes",
			"get_protein_atlas_gene", "search_protein_atlas", "map_string_ids",
			"get_string_network", "get_string_similarity_scores", "get_string_best_similarity_hits",
		},
	},
	{
		Slug:         "bio-tools-genes-ontologies",
		Name:         "Bio-Tools: UniProt & GO & Reactome & KEGG (基因功能本体与代谢通路)",
		ClientName:   "bio_genes_ontologies",
		EndpointSlug: "genes-ontologies",
		PackageName:  "mcp_genes_ontologies",
		Description:  "整合 MyGene.info 基因注释、EMBL-EBI OLS 本体检索（GO/HP/MONDO/UBERON）、QuickGO 功能注释、UniProtKB 蛋白条目、Reactome 信号通路映射与 KEGG 代谢通路。免凭证即用，独立 HTTP 路径：/mcp/genes-ontologies",
		Subcategory:  "蛋白结构与通路",
		DocsURL:      "https://www.uniprot.org/",
		Tags:         []string{"scientific", "pathways", "uniprot", "gene-ontology", "reactome", "kegg"},
		Tools: []string{
			"query_genes", "list_ontologies", "search_ontology_terms", "get_ontology_term",
			"get_go_annotations", "get_uniprot_entries", "map_reactome_pathways",
			"get_kegg_entries", "search_kegg", "link_kegg_ids",
		},
	},
	{
		Slug:         "bio-tools-biomart",
		Name:         "Bio-Tools: Ensembl BioMart (跨数据库基因/蛋白 ID 批量转换)",
		ClientName:   "bio_biomart",
		EndpointSlug: "biomart",
		PackageName:  "mcp_biomart",
		Description:  "对接 Ensembl BioMart 数据仓库，支持跨物种数据集浏览、Ensembl/Entrez/HGNC/UniProt/RefSeq 标识符单个或批量互转及自定义属性表导出。免凭证即用，独立 HTTP 路径：/mcp/biomart",
		Subcategory:  "蛋白结构与通路",
		DocsURL:      "https://www.ensembl.org/info/data/biomart/index.html",
		Tags:         []string{"scientific", "genomics", "biomart", "id-mapping", "ensembl"},
		Tools: []string{
			"list_marts", "list_datasets", "list_common_attributes", "list_all_attributes",
			"list_filters", "get_translation", "batch_translate", "get_data",
		},
	},
	{
		Slug:         "bio-tools-expression",
		Name:         "Bio-Tools: GTEx & PanglaoDB (人体组织表达谱、eQTL 与单细胞 Marker)",
		ClientName:   "bio_expression",
		EndpointSlug: "expression",
		PackageName:  "mcp_expression",
		Description:  "对接 GTEx Portal（人体 54 种正常组织基因中位表达量、组织特异性最高表达基因、单组织/多组织 eQTL 计算）与 PanglaoDB 单细胞测序细胞类型 Marker 基因库。免凭证即用，独立 HTTP 路径：/mcp/expression",
		Subcategory:  "表达、单细胞与调控",
		DocsURL:      "https://gtexportal.org/",
		Tags:         []string{"scientific", "transcriptomics", "gtex", "eqtl", "panglaodb", "single-cell"},
		Tools: []string{
			"gtex_tissue_sites", "gtex_dataset_info", "gtex_sample_info", "gtex_resolve_genes",
			"gtex_median_expression", "gtex_expression_summary", "gtex_gene_expression",
			"gtex_top_expressed_genes", "gtex_eqtl_genes", "gtex_single_tissue_eqtls",
			"gtex_multi_tissue_eqtls", "gtex_calculate_eqtl", "panglaodb_marker_genes",
			"panglaodb_options", "panglaodb_cell_types_for_gene",
		},
	},
	{
		Slug:         "bio-tools-cellguide",
		Name:         "Bio-Tools: CZ CELLxGENE CellGuide (单细胞标准细胞类型图谱)",
		ClientName:   "bio_cellguide",
		EndpointSlug: "cellguide",
		PackageName:  "mcp_cellguide",
		Description:  "对接 Chan Zuckerberg CELLxGENE CellGuide，查询标准细胞类型（Cell Ontology）定义、同义词、经典与计算 Marker 基因、所属解剖组织及来源单细胞数据集。免凭证即用，独立 HTTP 路径：/mcp/cellguide",
		Subcategory:  "表达、单细胞与调控",
		DocsURL:      "https://cellxgene.cziscience.com/cellguide",
		Tags:         []string{"scientific", "single-cell", "cellxgene", "cellguide", "marker-genes"},
		Tools: []string{
			"get_cell_type_info", "search_cell_types", "get_marker_genes", "get_source_data", "get_cell_tissues",
		},
	},
	{
		Slug:         "bio-tools-regulation",
		Name:         "Bio-Tools: ENCODE & JASPAR & UniBind (转录因子结合与表观调控)",
		ClientName:   "bio_regulation",
		EndpointSlug: "regulation",
		PackageName:  "mcp_regulation",
		Description:  "整合 JASPAR（转录因子结合位点 PWM/PFM 矩阵）、UniBind（实验验证的直接 TF-DNA 结合位点数据库）与 ENCODE 表观遗传学实验数据检索。免凭证即用，独立 HTTP 路径：/mcp/regulation",
		Subcategory:  "表达、单细胞与调控",
		DocsURL:      "https://jaspar.elixir.no/",
		Tags:         []string{"scientific", "epigenetics", "jaspar", "unibind", "encode", "transcription-factors"},
		Tools: []string{
			"jaspar_get_matrix", "jaspar_matrix_versions", "jaspar_list_matrices", "jaspar_list_species",
			"jaspar_list_taxa", "jaspar_list_collections", "jaspar_list_releases",
			"unibind_search_tfbs", "unibind_get_dataset", "unibind_tfbs_in_region",
			"encode_search_experiments", "encode_search_biosamples", "encode_list_files",
			"encode_get_experiment", "encode_get_file", "encode_get_biosample",
		},
	},
	{
		Slug:         "bio-tools-omics-archives",
		Name:         "Bio-Tools: GEO & PRIDE & MetaboLights & MGnify (五大组学公共归档库)",
		ClientName:   "bio_omics_archives",
		EndpointSlug: "omics-archives",
		PackageName:  "mcp_omics_archives",
		Description:  "跨组学公共实验数据检索：NCBI GEO 与 EBI ArrayExpress（转录组/表观组 Series）、PRIDE（质谱蛋白质组学项目）、MetaboLights（代谢组学研究）与 MGnify（微生物宏基因组分析）。独立 HTTP 路径：/mcp/omics-archives",
		Subcategory:  "表达、单细胞与调控",
		DocsURL:      "https://www.ebi.ac.uk/pride/",
		Tags:         []string{"scientific", "multi-omics", "geo", "arrayexpress", "pride", "metabolights", "mgnify"},
		OptionalEnvs: []string{"NCBI_EMAIL"},
		EnvHints: map[string]string{
			"NCBI_EMAIL": "可选：查询 NCBI GEO Series 时的 E-utilities 联系邮箱（留空不影响使用）",
		},
		Tools: []string{
			"geo_search_series", "geo_get_series", "arrayexpress_search_experiments",
			"arrayexpress_get_experiment", "arrayexpress_get_experiment_files", "arrayexpress_get_experiment_samples",
			"pride_search_projects", "pride_get_projects", "pride_search_project_proteins",
			"pride_find_projects_for_protein", "metabolights_list_studies", "metabolights_get_studies",
			"metabolights_get_study_files", "metabolights_search_data_files",
			"mgnify_search_studies", "mgnify_get_studies", "mgnify_get_study_analyses",
		},
	},
	{
		Slug:         "bio-tools-rna",
		Name:         "Bio-Tools: Rfam (非编码 RNA 家族与二级结构数据库)",
		ClientName:   "bio_rna",
		EndpointSlug: "rna",
		PackageName:  "mcp_rna",
		Description:  "对接 EMBL-EBI Rfam 数据库，查询非编码 RNA (ncRNA) 家族详情、种子多序列比对（Seed Alignment）、二级结构协方差模型（CM）、系统发育树与三维结构映射。免凭证即用，独立 HTTP 路径：/mcp/rna",
		Subcategory:  "表达、单细胞与调控",
		DocsURL:      "https://rfam.org/",
		Tags:         []string{"scientific", "rna", "rfam", "ncrna", "secondary-structure"},
		Tools: []string{
			"get_family", "get_seed_alignment", "get_covariance_model", "get_tree",
			"get_sequence_regions", "get_structure_mapping", "accession_to_id", "id_to_accession", "search_sequence",
		},
	},
	{
		Slug:         "bio-tools-cancer-models",
		Name:         "Bio-Tools: DepMap & cBioPortal (肿瘤细胞系 CRISPR 依赖性与临床突变队列)",
		ClientName:   "bio_cancer_models",
		EndpointSlug: "cancer-models",
		PackageName:  "mcp_cancer_models",
		Description:  "整合 Sanger Cell Model Passports / DepMap（肿瘤细胞系与类器官模型、CRISPR 基因敲除依赖性评分）与 cBioPortal（临床癌症基因组研究、基因体细胞突变率、拷贝数变异 CNA 与临床属性）。免凭证即用，独立 HTTP 路径：/mcp/cancer-models",
		Subcategory:  "肿瘤、临床与科研资源",
		DocsURL:      "https://www.cbioportal.org/",
		Tags:         []string{"scientific", "oncology", "depmap", "cbioportal", "crispr", "cancer"},
		Tools: []string{
			"list_models", "get_model", "search_models", "gene_dependencies", "search_genes",
			"cbioportal_list_studies", "cbioportal_get_study", "cbioportal_mutations_in_gene",
			"cbioportal_mutation_frequency", "cbioportal_cna_in_gene", "cbioportal_clinical_attributes",
		},
	},
	{
		Slug:         "bio-tools-clinical-trials",
		Name:         "Bio-Tools: ClinicalTrials.gov (全球临床试验注册与管线检索)",
		ClientName:   "bio_clinical_trials",
		EndpointSlug: "clinical-trials",
		PackageName:  "mcp_clinical_trials",
		Description:  "对接 ClinicalTrials.gov API v2，支持按疾病/药物/状态检索全球临床试验、获取 NCT 试验完整方案、按入组/排他标准筛选患者匹配试验、按药企申办方盘点管线及终点分析。免凭证即用，独立 HTTP 路径：/mcp/clinical-trials",
		Subcategory:  "肿瘤、临床与科研资源",
		DocsURL:      "https://clinicaltrials.gov/",
		Tags:         []string{"scientific", "clinical-trials", "medicine", "nct", "pharma"},
		Tools: []string{
			"search_trials", "get_trial_details", "search_by_sponsor",
			"search_by_eligibility", "search_investigators", "analyze_endpoints",
		},
	},
	{
		Slug:         "bio-tools-research-resources",
		Name:         "Bio-Tools: Grants.gov & Antibody Registry (科研基金与实验抗体核验)",
		ClientName:   "bio_research_resources",
		EndpointSlug: "research-resources",
		PackageName:  "mcp_research_resources",
		Description:  "对接美国联邦科研基金库 Grants.gov（检索 NIH/NSF 等在研资助机会）与 Antibody Registry（按靶点蛋白、克隆号、货号检索实验抗体并核验标准 RRID 唯一标识）。免凭证即用，独立 HTTP 路径：/mcp/research-resources",
		Subcategory:  "肿瘤、临床与科研资源",
		DocsURL:      "https://www.antibodyregistry.org/",
		Tags:         []string{"scientific", "antibodies", "rrid", "grants", "reagents"},
		Tools: []string{
			"search_grants", "search_antibodies", "get_antibody",
			"find_antibodies_by_catalog", "get_antibody_registry_stats",
		},
	},
}

// SeedBuiltinScientificMCPLibrary upserts the 24 built-in bio-tools scientific MCP
// server templates into the mcp_library catalog table. Tombstoned (deleted_at != nil)
// or user-customized rows are respected.
func SeedBuiltinScientificMCPLibrary(ctx context.Context, store configstore.ConfigStore) (int, error) {
	if store == nil {
		return 0, nil
	}
	protected, err := store.GetProtectedMCPLibrarySlugs(ctx)
	if err != nil {
		return 0, err
	}
	protectedSet := make(map[string]bool, len(protected))
	for _, s := range protected {
		protectedSet[s] = true
	}

	pythonBin, runnerScript := ResolveBioToolsPaths()
	now := time.Now()
	count := 0

	err = store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		for _, def := range builtinScientificMCPDefs {
			if protectedSet[def.Slug] {
				continue
			}
			envs := make([]string, len(def.OptionalEnvs))
			copy(envs, def.OptionalEnvs)

			metadata := map[string]any{
				"client_name":           def.ClientName,
				"endpoint_slug":         def.EndpointSlug,
				"package_name":          def.PackageName,
				"subcategory":           def.Subcategory,
				"tool_count":            len(def.Tools),
				"tools":                 def.Tools,
				"optional_envs":         def.OptionalEnvs,
				"env_hints":             def.EnvHints,
				"recommended_code_mode": def.RecommendedCodeMode,
				"allow_by_default":      true,
				"http_endpoint_path":    "/mcp/" + def.EndpointSlug,
			}
			if def.Slug == "bio-tools-all-in-one" {
				metadata["tool_count"] = 232
			}

			row := &configstoreTables.TableMCPLibrary{
				Slug:           def.Slug,
				Name:           def.Name,
				Description:    def.Description,
				Category:       ScientificDiscoveryCategory,
				ConnectionType: schemas.MCPConnectionTypeSTDIO,
				StdioConfig: &schemas.MCPStdioConfig{
					Command: pythonBin,
					Args:    []string{runnerScript, def.PackageName},
					Envs:    envs,
				},
				AuthType:  schemas.MCPAuthTypeNone,
				DocsURL:   def.DocsURL,
				Publisher: "Tumbleweed Scientific",
				Tags:      def.Tags,
				Metadata:  metadata,
				Source:    "remote",
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := store.UpsertMCPLibraryEntry(ctx, row, tx); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}
