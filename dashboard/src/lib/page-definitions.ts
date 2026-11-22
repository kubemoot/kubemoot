// One definition per dashboard page: the page title, what the term means in Kubemoot,
// and what a person can do on the page. PageTitle reads these; a test walks
// src/routes and fails when a page has no entry.

export interface PageDefinition {
	/** The heading shown on the page (detail pages show the resource name instead). */
	title: string;
	/** What the term means in Kubemoot. */
	term: string;
	/** What you can do on this page. */
	purpose: string;
	/** The left-menu label, when it is shorter than the title. */
	menu?: string;
}

const TERMS = {
	crews: 'A crew is a team of agents that deliberate together to answer questions in one domain.',
	agents: 'An agent is one member of a crew, backed by a model, with its own prompt and tools.',
	promptmodules: 'A prompt module is a reusable block of instructions that agents combine in order.',
	crewfitnesses: 'A fitness test is one run of a scripted scenario against a crew.',
	models: 'A model is a language model that agents call, loaded by a model server.',
	modelservers: 'A model server is the inference backend, such as vLLM, that runs models on an accelerator.',
	embeddingmodels: 'An embedding model turns text into vectors so agents can search documents by meaning.',
	mcpservers: 'An MCP server gives agents tools, such as kubectl or Helm, through the Model Context Protocol.',
	mcpgateways: 'An MCP gateway is the single endpoint agents use to reach a crew\'s MCP servers.',
	mcpcatalogs: 'An MCP catalog lists MCP servers found in an outside registry.',
	mcpqualitypolicies: 'A quality policy is a set of allow and block rules for MCP servers found in catalogs.',
	mcpreports: 'An MCP report is the test history and reliability verdict for one MCP server.',
	ragsources: 'A RAG source is a document repository indexed so agents can search it for grounded answers.'
};

export const PAGE_DEFINITIONS = {
	'/signin': {
		title: 'Sign in to Kubemoot',
		menu: 'Sign in',
		term: 'Signing in tells the dashboard who you are, using a Kubernetes bearer token.',
		purpose: 'Paste a token so the dashboard acts as you and your own permissions apply.'
	},
	'/': {
		title: 'Overview',
		term: 'The overview is the summary of everything Kubemoot manages on this cluster.',
		purpose: 'See what is healthy and what needs attention, then open a tile to find out why.'
	},
	'/nodes': {
		title: 'Kubernetes Nodes',
		menu: 'Nodes',
		term: "A Kubernetes node is a machine, physical or virtual, that runs the cluster's workloads.",
		purpose: "See which nodes are unwell and how much CPU, memory and accelerator each one uses."
	},
	'/nodes/[name]': {
		title: 'Kubernetes Node',
		term: "A Kubernetes node is a machine, physical or virtual, that runs the cluster's workloads.",
		purpose: "Read this node's conditions, use, accelerators and pods, and its YAML."
	},
	'/conversations': {
		title: 'Conversations',
		term: 'A conversation holds your own questions to a crew and the crew\'s answers.',
		purpose: 'Ask a question, follow up, stop an answer in progress, or delete a conversation.'
	},
	'/moot': {
		title: 'The Moot',
		menu: 'Moot',
		term: 'A moot is the agents of a crew deliberating over one question until they settle on an answer.',
		purpose: 'Read how the agents reached each answer, message by message. This page is read-only.'
	},
	'/crews': {
		title: 'Crews',
		term: TERMS.crews,
		purpose: 'See each crew and its state, and open one to see its agents and memory.'
	},
	'/crews/[name]': {
		title: 'Crew',
		term: TERMS.crews,
		purpose: 'Read this crew\'s state, version and agents, and its YAML.'
	},
	'/agents': {
		title: 'Agents',
		term: TERMS.agents,
		purpose: 'Browse agents by role and check which ones are alive.'
	},
	'/agents/[name]': {
		title: 'Agent',
		term: TERMS.agents,
		purpose: 'Read this agent\'s spec, prompt modules and status, and its YAML.'
	},
	'/promptmodules': {
		title: 'Prompt Modules',
		menu: 'Prompts',
		term: TERMS.promptmodules,
		purpose: 'Read the instruction modules agents are built from.'
	},
	'/promptmodules/[name]': {
		title: 'Prompt Module',
		term: TERMS.promptmodules,
		purpose: 'Read this module\'s text and its place in the order.'
	},
	'/crew-memory': {
		title: 'Crew Memory',
		menu: 'Memory',
		term: 'Crew memory is what a crew has learned about its cluster and keeps between conversations.',
		purpose: 'Read what a crew remembers and export it.'
	},
	'/topology': {
		title: 'Agent Topology',
		term: 'Topology is the map of how a crew\'s agents relate to one another.',
		purpose: 'See coordinators, specialists and peers, and drag them to arrange the map.'
	},
	'/fitness': {
		title: 'Fitness',
		term: 'Fitness measures how well a crew answers a set of scripted scenarios.',
		purpose: 'Follow fitness suites and tests, and read each result and its evidence.'
	},
	'/crewfitnesses': {
		title: 'Fitness Tests',
		term: TERMS.crewfitnesses,
		purpose: 'See each run\'s phase and result, and open one for details.'
	},
	'/crewfitnesses/[name]': {
		title: 'Fitness Test',
		term: TERMS.crewfitnesses,
		purpose: 'Read this run\'s spec, status and result, and its YAML.'
	},
	'/crewfitnesssuites': {
		title: 'Fitness Suites',
		term: 'A fitness suite repeats a crew\'s scenarios many times to measure how reliably it answers.',
		purpose: 'Follow each suite\'s progress and pass rates.'
	},
	'/crewfitnesssuites/[name]': {
		title: 'Fitness Suite',
		term: 'A fitness suite repeats a crew\'s scenarios many times to measure how reliably it answers.',
		purpose: 'Read this suite\'s spec, status and counts, and its YAML.'
	},
	'/models': {
		title: 'Chat Models',
		menu: 'Models',
		term: TERMS.models,
		purpose: 'See which models exist, what they can do and where they are placed.'
	},
	'/models/[name]': {
		title: 'Model',
		term: TERMS.models,
		purpose: 'Read this model\'s spec and status, and its YAML.'
	},
	'/modelservers': {
		title: 'Model Servers',
		menu: 'Model servers',
		term: TERMS.modelservers,
		purpose: 'Check each server\'s connection and the models it holds.'
	},
	'/modelservers/[name]': {
		title: 'Model Server',
		term: TERMS.modelservers,
		purpose: 'Read this server\'s spec, status and loaded models, and its YAML.'
	},
	'/embeddingmodels': {
		title: 'Embedding Models',
		menu: 'Embeddings',
		term: TERMS.embeddingmodels,
		purpose: 'See which embedding models exist and whether each is ready.'
	},
	'/embeddingmodels/[name]': {
		title: 'Embedding Model',
		term: TERMS.embeddingmodels,
		purpose: 'Read this model\'s spec and status, and its YAML.'
	},
	'/mcpservers': {
		title: 'MCP Servers',
		menu: 'MCP servers',
		term: TERMS.mcpservers,
		purpose: 'Check each tool server\'s health and open one to see its tools.'
	},
	'/mcpservers/[name]': {
		title: 'MCP Server',
		term: TERMS.mcpservers,
		purpose: 'Read this server\'s spec, status and tools, and its YAML.'
	},
	'/mcpgateways': {
		title: 'MCP Gateways',
		menu: 'MCP gateways',
		term: TERMS.mcpgateways,
		purpose: 'See which servers each gateway routes to and whether it is healthy.'
	},
	'/mcpgateways/[name]': {
		title: 'MCP Gateway',
		term: TERMS.mcpgateways,
		purpose: 'Read this gateway\'s spec and status, and its YAML.'
	},
	'/mcpcatalogs': {
		title: 'MCP Catalogs',
		menu: 'Catalogs',
		term: TERMS.mcpcatalogs,
		purpose: 'See which registries Kubemoot searches and what each catalog found.'
	},
	'/mcpcatalogs/[name]': {
		title: 'MCP Catalog',
		term: TERMS.mcpcatalogs,
		purpose: 'Read this catalog\'s queries and status, and its YAML.'
	},
	'/mcpqualitypolicies': {
		title: 'MCP Quality Policies',
		menu: 'Quality policies',
		term: TERMS.mcpqualitypolicies,
		purpose: 'Read the rules that decide which discovered servers may be deployed.'
	},
	'/mcpqualitypolicies/[name]': {
		title: 'Quality Policy',
		term: TERMS.mcpqualitypolicies,
		purpose: 'Read this policy\'s allow and block rules, and its YAML.'
	},
	'/mcpreports': {
		title: 'MCP Server Reports',
		menu: 'Reports',
		term: TERMS.mcpreports,
		purpose: 'Compare servers by verdict and open a report to see its trials.'
	},
	'/mcpreports/[name]': {
		title: 'MCP Report',
		term: TERMS.mcpreports,
		purpose: 'Read this server\'s verdict and every trial behind it.'
	},
	'/ragsources': {
		title: 'RAG Sources',
		term: TERMS.ragsources,
		purpose: 'See each source\'s repository and whether its index is ready.'
	},
	'/ragsources/[name]': {
		title: 'RAG Source',
		term: TERMS.ragsources,
		purpose: 'Read this source\'s repository, vector store and status, and its YAML.'
	},
	'/config': {
		title: 'Kubemoot Config',
		menu: 'Config',
		term: 'Kubemoot config holds the cluster-wide settings that apply to every crew.',
		purpose: 'Read the default images and display preferences the operator uses.'
	},
	'/nats': {
		title: 'NATS',
		term: 'NATS is the message bus where agents deliberate and where Kubemoot keeps its state.',
		purpose: 'Read server, stream and per-crew storage figures. This page changes nothing.'
	}
} as const satisfies Record<string, PageDefinition>;

export type PageRoute = keyof typeof PAGE_DEFINITIONS;

/** The left-menu label for a page: its menu label, else its title. */
export function menuLabel(route: PageRoute): string {
	const def: PageDefinition = PAGE_DEFINITIONS[route];
	return def.menu ?? def.title;
}

export function pageDefinition(route: PageRoute): PageDefinition {
	return PAGE_DEFINITIONS[route];
}
