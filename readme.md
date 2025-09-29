## Scalpel-CLI Overview

Scalpel-CLI is an AI-native security scanner written in Go that performs automated security testing on web applications. It's a comprehensive security analysis tool combining browser automation, vulnerability detection, and AI-powered analysis.

### Core Architecture

#### Key Components:

1. Browser Automation (internal/browser/)
   - Headless browser control via ChromeDP and Playwright
   - Humanoid interaction simulation for realistic browsing
   - Session management, stealth techniques, and DOM manipulation
   - Network interception and HAR capture capabilities
2. Security Analysis (internal/analysis/)
   - Active scanners: Taint analysis, prototype pollution, time-slip attacks
   - Passive scanners: Header analysis, JWT vulnerability detection
   - Auth testing: Account takeover (ATO), IDOR vulnerabilities
   - Static analysis: JWT secret detection and brute-forcing
3. AI Agent (internal/agent/)
   - LLM-powered security analysis using AI models
   - Knowledge graph integration for context tracking
   - Automated decision-making for security testing
4. Discovery Engine (internal/discovery/)
   - Automatic endpoint and parameter discovery
   - Crawling and mapping of web applications
5. Orchestrator (internal/orchestrator/)
   - Coordinates scanning workflow
   - Task scheduling and worker management
   - Result aggregation
6. Reporting (internal/reporting/)
   - SARIF format support for security findings
   - Structured vulnerability reports

#### Key Features

- Multi-layered scanning: Combines passive observation, active testing, and AI analysis
- Browser automation: Realistic user interaction simulation with anti-detection
- Authentication testing: Specialized modules for ATO and IDOR vulnerabilities
- Configurable: Extensive YAML configuration for scanner behavior
- Database-backed: PostgreSQL integration for persistent storage
- Concurrent execution: Worker-based architecture for parallel scanning

## Usage

The CLI provides two main commands:
- scalpel-cli scan [targets...] - Initiates security scanning
- scalpel-cli report - Generates security reports

The tool is designed for security professionals to identify vulnerabilities in web applications through comprehensive automated testing.
