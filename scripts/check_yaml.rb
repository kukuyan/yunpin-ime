#!/usr/bin/env ruby
# SPDX-License-Identifier: Apache-2.0

require "yaml"

root = File.expand_path("..", __dir__)
excluded = [".git/", ".cache/", "build/", "dist/", "third_party/"]
paths = Dir.glob(File.join(root, "**", "*.{yml,yaml}"), File::FNM_DOTMATCH).sort.reject do |path|
  relative = path.delete_prefix(root + "/")
  excluded.any? { |prefix| relative.start_with?(prefix) }
end

errors = []
paths.each do |path|
  begin
    YAML.parse_file(path)
  rescue StandardError => error
    errors << "#{path.delete_prefix(root + "/")}: #{error.class}: #{error.message.lines.first.to_s.strip}"
  end
end

openapi_path = File.join(root, "sync", "openapi.yaml")
begin
  content = File.read(openapi_path, encoding: "UTF-8")
  begin
    openapi = YAML.safe_load(content, permitted_classes: [], permitted_symbols: [], aliases: true)
  rescue ArgumentError
    openapi = YAML.safe_load(content, [], [], true)
  end
  unless openapi.is_a?(Hash) && openapi["openapi"].to_s.match?(/\A3\.\d+\.\d+\z/)
    errors << "sync/openapi.yaml: missing OpenAPI 3.x version"
  end
  info = openapi.is_a?(Hash) ? openapi["info"] : nil
  unless info.is_a?(Hash) && !info["title"].to_s.empty? && !info["version"].to_s.empty?
    errors << "sync/openapi.yaml: info.title and info.version are required"
  end
  operations = %w[get put post delete patch options head trace]
  api_paths = openapi.is_a?(Hash) ? openapi["paths"] : nil
  if !api_paths.is_a?(Hash) || api_paths.empty?
    errors << "sync/openapi.yaml: paths must be a non-empty mapping"
  else
    api_paths.each do |route, item|
      errors << "sync/openapi.yaml: invalid route #{route.inspect}" unless route.to_s.start_with?("/")
      next unless item.is_a?(Hash)
      item.each do |method, operation|
        next unless operations.include?(method.to_s.downcase)
        unless operation.is_a?(Hash) && operation["responses"].is_a?(Hash) && !operation["responses"].empty?
          errors << "sync/openapi.yaml: #{method.to_s.upcase} #{route} lacks responses"
        end
      end
    end
  end
rescue StandardError => error
  errors << "sync/openapi.yaml: semantic parse failed: #{error.class}: #{error.message.lines.first.to_s.strip}"
end

unless errors.empty?
  warn errors.join("\n")
  exit 1
end

puts "YAML syntax passed: #{paths.length} files; OpenAPI structure passed"
