// sonar-oracle runs Sonar's own config upgraders (from the installer's
// apps/sonar assemblies) over configs exported by the Go test, recording the
// results internal/sonar's upgrades are checked against:
//
//	GG_LINUX_DATA=<data> GG_TEST_SONAR_EXPORT=configs.json go test ./internal/sonar -run TestUpgradeMatchesSonar
//	dotnet run --project tools/sonar-oracle -p:SonarDir=<installer>/apps/sonar -- <installer>/apps/sonar configs.json oracle.json
//	GG_LINUX_DATA=<data> GG_TEST_SONAR_ORACLE=oracle.json go test ./internal/sonar -run TestUpgradeMatchesSonar
using System.Reflection;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

var dir = args[0];
AppDomain.CurrentDomain.AssemblyResolve += (_, e) =>
{
    var p = Path.Combine(dir, new AssemblyName(e.Name).Name + ".dll");
    return File.Exists(p) ? Assembly.LoadFrom(p) : null;
};
var services = Assembly.LoadFrom(Path.Combine(dir, "SoundStage.Services.dll"));
var models = Assembly.LoadFrom(Path.Combine(dir, "SoundStage.Models.dll"));
var logging = Assembly.LoadFrom(Path.Combine(dir, "Sonar.Logging.dll"));
var logger = DispatchProxy.Create(logging.GetType("SoundStage.Logging.ILogger", true), typeof(Nop));
const string ns = "SoundStage.Services.Configs.Upgrader.VadConfig.";
var upgraders = new Dictionary<int, object>
{
    [1] = Activator.CreateInstance(services.GetType(ns + "Game.GameConfigUpgrader", true), logger),
    [2] = Activator.CreateInstance(services.GetType(ns + "ChatRender.ChatRenderConfigUpgrader", true), logger),
    [3] = Activator.CreateInstance(services.GetType(ns + "ChatCapture.ChatCaptureConfigUpgrader", true), logger),
    [4] = Activator.CreateInstance(services.GetType(ns + "Media.MediaConfigUpgrader", true), logger),
    [5] = Activator.CreateInstance(services.GetType(ns + "Auxiliary.AuxConfigUpgrader", true), logger),
};
var configType = models.GetType("SoundStage.Models.Configs.Config", true);
var vadType = models.GetType("SoundStage.Models.Configs.VirtualAudioDevice", true);
var output = new JArray();
foreach (JObject row in JArray.Parse(File.ReadAllText(args[1])))
{
    int vad = (int)row["vad"];
    var up = upgraders[vad];
    int latest = (int)up.GetType().GetProperty("LatestVersion").GetValue(up);
    var cfg = Activator.CreateInstance(configType);
    void Set(string name, object v) => configType.GetProperty(name).SetValue(cfg, v);
    Set("Id", Guid.Parse((string)row["id"]));
    Set("Name", (string)row["name"]);
    Set("VirtualAudioDevice", Enum.ToObject(vadType, vad));
    Set("SchemaVersion", (int)row["schema_version"]);
    Set("IsPreset", (int)row["is_preset"] != 0);
    Set("Image", (string)row["image"]);
    Set("Data", JObject.Parse((string)row["data"]));
    Set("DefaultData", JObject.Parse((string)row["default_data"]));
    var result = new JObject { ["id"] = row["id"], ["vad"] = vad, ["from"] = row["schema_version"] };
    try
    {
        var outCfg = (int)row["schema_version"] < latest ? up.GetType().GetMethod("Upgrade").Invoke(up, new[] { cfg }) : cfg;
        result["to"] = (int)configType.GetProperty("SchemaVersion").GetValue(outCfg);
        result["image"] = (string)configType.GetProperty("Image").GetValue(outCfg);
        result["data"] = JToken.FromObject(configType.GetProperty("Data").GetValue(outCfg));
        result["defaultData"] = JToken.FromObject(configType.GetProperty("DefaultData").GetValue(outCfg));
    }
    catch (Exception ex)
    {
        result["error"] = (ex.InnerException ?? ex).Message;
    }
    output.Add(result);
}
var conv = services.GetType("SoundStage.Services.Configs.GenericConfigToVadConfigConverter", true).GetMethod("Convert");
var generic = new JObject();
for (int v = 1; v <= 5; v++)
    generic[v.ToString()] = JToken.FromObject(conv.Invoke(null, new object[] { Enum.ToObject(vadType, v), "" }));
File.WriteAllText(args[2], new JObject { ["configs"] = output, ["generic"] = generic }.ToString(Formatting.Indented));
Console.WriteLine($"{output.Count} configs");

public class Nop : DispatchProxy
{
    protected override object Invoke(MethodInfo m, object[] a) =>
        m.ReturnType != typeof(void) && m.ReturnType.IsValueType ? Activator.CreateInstance(m.ReturnType) : null;
}
